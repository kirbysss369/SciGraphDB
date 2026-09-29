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
	if err != nil || len(states) != 5 || states[0].Applied || states[1].Applied || states[2].Applied || states[3].Applied || states[4].Applied {
		t.Fatalf("requires an unmigrated disposable database: states=%v err=%v", states, err)
	}
	if _, err := migrations.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for range 5 {
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
	outDir := t.TempDir()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/bench", "--config",
		"experiments/configs/hnsw-index-ci-v1.json", "--out-dir", outDir)
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dsn)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bench smoke with 3000 generated vectors: %v\n%s", err, output)
	}
	var summary struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(output, &summary); err != nil || summary.RunID == "" {
		t.Fatalf("bench summary: %v; %s", err, output)
	}
	reportJSON, err := os.ReadFile(filepath.Join(outDir, summary.RunID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("BENCH_REPORT_PATH"); path != "" {
		if err := os.WriteFile(path, reportJSON, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var report struct {
		Papers          int      `json:"paper_count"`
		EligibleVectors int      `json:"eligible_count"`
		BuildOptions    []string `json:"index_build_options"`
		Queries         []struct {
			QueryID   string  `json:"query_id"`
			K         int     `json:"k"`
			Method    string  `json:"method"`
			Recall    float64 `json:"mean_recall_at_k"`
			IndexUsed bool    `json:"index_used"`
			P50MS     float64 `json:"p50_ms"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(reportJSON, &report); err != nil || report.Papers != 3008 || report.EligibleVectors != 3005 || len(report.Queries) != 12 {
		t.Fatalf("bench report: %v; papers=%d eligible=%d queries=%d", err, report.Papers, report.EligibleVectors, len(report.Queries))
	}
	for _, row := range report.Queries {
		if (row.Method == "hnsw" && !row.IndexUsed) || (row.Method == "exact" && row.IndexUsed) || row.Recall < 0 || row.Recall > 1 {
			t.Fatalf("invalid measurement: %+v", row)
		}
		t.Logf("synthetic papers=%d eligible=%d build=%v query=%s k=%d method=%s recall=%.3f p50_ms=%.3f index_used=%v",
			report.Papers, report.EligibleVectors, report.BuildOptions,
			row.QueryID, row.K, row.Method, row.Recall, row.P50MS, row.IndexUsed)
	}
}
