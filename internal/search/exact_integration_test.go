//go:build integration

package search

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kirbysss369/SciGraphDB/db/migrations"
)

type baseline struct {
	Version       string `json:"version"`
	ModelID       string `json:"model_id"`
	ModelRevision string `json:"model_revision"`
	TextVersion   string `json:"text_version"`
	Papers        []struct {
		ID              int64  `json:"id"`
		OpenAlexID      string `json:"openalex_id"`
		Title           string `json:"title"`
		PublicationYear *int   `json:"publication_year"`
		Axis            *int   `json:"axis"`
		Sign            int    `json:"sign"`
		Zero            bool   `json:"zero"`
		Stale           bool   `json:"stale"`
	} `json:"papers"`
	Queries []struct {
		Name     string `json:"name"`
		File     string `json:"file"`
		Limit    int    `json:"limit"`
		Expected []struct {
			ID       int64   `json:"id"`
			Distance float64 `json:"distance"`
		} `json:"expected"`
	} `json:"queries"`
}

func TestExactTopKBaseline(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("set DATABASE_URL_TEST to an empty disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	states, err := migrations.Status(ctx, conn)
	if err != nil || len(states) != 4 || states[0].Applied || states[1].Applied || states[2].Applied || states[3].Applied {
		t.Fatalf("requires an unmigrated disposable database: states=%v err=%v", states, err)
	}
	if _, err := migrations.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for range 4 {
			if _, err := migrations.Down(cleanup, conn); err != nil {
				t.Errorf("cleanup migration: %v", err)
			}
		}
	}()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	root := filepath.Join("..", "..", "experiments", "exact_v1")
	data, err := os.ReadFile(filepath.Join(root, "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture baseline
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != "exact-v1" || fixture.ModelID != ModelID || fixture.ModelRevision != ModelRevision || fixture.TextVersion != TextVersion {
		t.Fatal("baseline model or version does not match search")
	}
	for _, paper := range fixture.Papers {
		if paper.Axis == nil && !paper.Zero {
			_, err = pool.Exec(ctx, `INSERT INTO public.papers (id, openalex_id, title, publication_year)
				VALUES ($1, $2, $3, $4)`, paper.ID, paper.OpenAlexID, paper.Title, paper.PublicationYear)
		} else {
			vector := make([]float64, Dimension)
			if paper.Axis != nil {
				vector[*paper.Axis] = float64(paper.Sign)
			}
			literal := "[" + strings.TrimSuffix(strings.Repeat("0,", Dimension), ",") + "]"
			if !paper.Zero {
				literal, err = VectorLiteral(vector)
				if err != nil {
					t.Fatal(err)
				}
			}
			model := ModelID
			if paper.Stale {
				model = "stale-model"
			}
			_, err = pool.Exec(ctx, `INSERT INTO public.papers
				(id, openalex_id, title, publication_year, embedding, embedding_model,
				 embedding_revision, embedding_text_version, embedding_text_sha256, embedded_at)
				VALUES ($1, $2, $3, $4, $5::public.vector, $6, $7, $8, repeat('a', 64), now())`,
				paper.ID, paper.OpenAlexID, paper.Title, paper.PublicationYear, literal,
				model, ModelRevision, TextVersion)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range fixture.Queries {
		t.Run(query.Name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, query.File))
			if err != nil {
				t.Fatal(err)
			}
			var input struct {
				ModelID       string    `json:"model_id"`
				ModelRevision string    `json:"model_revision"`
				TextVersion   string    `json:"text_version"`
				Embedding     []float64 `json:"embedding"`
			}
			if err := json.Unmarshal(data, &input); err != nil || input.ModelID != ModelID || input.ModelRevision != ModelRevision || input.TextVersion != TextVersion {
				t.Fatalf("query vector metadata: %v", err)
			}
			for range 2 {
				results, err := Exact(ctx, pool, input.Embedding, query.Limit)
				if err != nil || len(results) != len(query.Expected) {
					t.Fatalf("results=%+v err=%v", results, err)
				}
				for i, want := range query.Expected {
					if results[i].ID != want.ID || math.Abs(results[i].Distance-want.Distance) > 1e-6 {
						t.Errorf("rank %d: got %+v, want %+v", i, results[i], want)
					}
				}
			}
			if query.Name == "positive-x" {
				literal, err := VectorLiteral(input.Embedding)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "+ExactSQL,
					literal, ModelID, ModelRevision, TextVersion, query.Limit)
				if err != nil {
					t.Fatal(err)
				}
				var plan []string
				for rows.Next() {
					var line string
					if err := rows.Scan(&line); err != nil {
						t.Fatal(err)
					}
					plan = append(plan, line)
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				output := strings.Join(plan, "\n")
				t.Log("EXPLAIN (ANALYZE, BUFFERS) on synthetic fixture:\n" + output)
				if !strings.Contains(output, "Seq Scan on papers") || !strings.Contains(output, "Buffers:") || !strings.Contains(output, "Sort Key:") || strings.Contains(output, HNSWIndexName) {
					t.Fatal("expected exact sequential scan and sort with buffer measurements")
				}
			}
		})
	}

	// The eight-row fixture is too small for the planner to prefer HNSW.
	// Grow this disposable corpus before checking the natural plan and CLI.
	_, err = pool.Exec(ctx, `INSERT INTO public.papers
		(id, openalex_id, title, embedding, embedding_model, embedding_revision,
		 embedding_text_version, embedding_text_sha256, embedded_at)
		SELECT n + 10000, 'W-bench-' || n, 'Bench ' || n,
		       ('[' || (n % 101)::text || ',' || ((n * 17) % 103)::text || ',' || repeat('0,', 381) || '1]')::public.vector,
		       $1, $2, $3, repeat('b', 64), now()
		FROM generate_series(1, 3000) AS n`, ModelID, ModelRevision, TextVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE public.papers`); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "go", "run", "../../cmd/bench", "--query-dir", root,
		"--repeats", "1", "--require-index")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dsn)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bench smoke with 3000 generated vectors: %v\n%s", err, output)
	}
	var report struct {
		Papers          int `json:"papers"`
		EligibleVectors int `json:"eligible_vectors"`
		Measurements    []struct {
			K             int     `json:"k"`
			Recall        float64 `json:"recall"`
			HNSWIndexUsed bool    `json:"hnsw_index_used"`
		} `json:"measurements"`
	}
	if err := json.Unmarshal(output, &report); err != nil || report.Papers != 3008 || report.EligibleVectors != 3005 || len(report.Measurements) != 6 {
		t.Fatalf("bench report: %v; %s", err, output)
	}
	for _, row := range report.Measurements {
		if !row.HNSWIndexUsed || row.Recall < 0 || row.Recall > 1 {
			t.Fatalf("invalid HNSW measurement: %+v", row)
		}
		t.Logf("bench synthetic 3005 eligible vectors k=%d recall=%.3f hnsw_index_used=%v", row.K, row.Recall, row.HNSWIndexUsed)
	}
}
