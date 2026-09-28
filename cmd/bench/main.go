// bench compares exact and HNSW retrieval on the same pinned query vectors.
// It never writes to the database. Run it after migrations and embedding.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kirbysss369/SciGraphDB/internal/database"
	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
	"github.com/kirbysss369/SciGraphDB/internal/search"
)

type queryFile struct {
	ModelID       string    `json:"model_id"`
	ModelRevision string    `json:"model_revision"`
	TextVersion   string    `json:"text_version"`
	Embedding     []float64 `json:"embedding"`
}

type manifest struct {
	Version string `json:"version"`
	Queries []struct {
		Name string `json:"name"`
		File string `json:"file"`
	} `json:"queries"`
}

type measurement struct {
	Query           string          `json:"query"`
	K               int             `json:"k"`
	ExactIDs        []int64         `json:"exact_ids"`
	HNSWIDs         []int64         `json:"hnsw_ids"`
	Recall          float64         `json:"recall"`
	ExactMedianMS   float64         `json:"exact_median_ms"`
	HNSWMedianMS    float64         `json:"hnsw_median_ms"`
	HNSWIndexUsed   bool            `json:"hnsw_index_used"`
	ExactPlan       json.RawMessage `json:"exact_plan"`
	HNSWPlan        json.RawMessage `json:"hnsw_plan"`
}

type report struct {
	FixtureVersion   string        `json:"query_fixture_version"`
	Papers           int64         `json:"papers"`
	EligibleVectors  int64         `json:"eligible_vectors"`
	ModelID          string        `json:"model_id"`
	ModelRevision    string        `json:"model_revision"`
	TextVersion      string        `json:"text_version"`
	PgvectorVersion  string        `json:"pgvector_version"`
	IndexName        string        `json:"index_name"`
	BuildOptions     []string      `json:"build_options"`
	EFSearch         int           `json:"ef_search"`
	Repeats          int           `json:"repeats"`
	Measurements     []measurement `json:"measurements"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("bench", flag.ContinueOnError)
	queryDir := flags.String("query-dir", "experiments/exact_v1", "directory with versioned query vectors")
	efSearch := flags.Int("ef-search", search.DefaultEFSearch, "HNSW query setting (1..1000)")
	repeats := flags.Int("repeats", 3, "timed runs per method and query")
	requireIndex := flags.Bool("require-index", false, "fail if HNSW plan does not use the index")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *repeats < 1 || *repeats > 100 {
		return errors.New("usage: bench [--query-dir DIR] [--ef-search 1..1000] [--repeats 1..100] [--require-index]")
	}
	if err := search.ValidateEFSearch(*efSearch); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(*queryDir, "baseline.json"))
	if err != nil {
		return err
	}
	var fixture manifest
	if err := json.Unmarshal(data, &fixture); err != nil || fixture.Version == "" || len(fixture.Queries) == 0 {
		return fmt.Errorf("invalid query manifest: %v", err)
	}
	if err := devconfig.Load(); err != nil {
		return err
	}
	dsn, err := devconfig.DatabaseURL()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := database.New(ctx, dsn, 3*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	result := report{FixtureVersion: fixture.Version, ModelID: search.ModelID,
		ModelRevision: search.ModelRevision, TextVersion: search.TextVersion,
		IndexName: search.HNSWIndexName, EFSearch: *efSearch, Repeats: *repeats}
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE embedding IS NOT NULL
		AND public.vector_norm(embedding) > 0 AND embedding_model = $1
		AND embedding_revision = $2 AND embedding_text_version = $3)
		FROM public.papers`, search.ModelID, search.ModelRevision, search.TextVersion).
		Scan(&result.Papers, &result.EligibleVectors); err != nil {
		return err
	}
	if result.EligibleVectors == 0 {
		return errors.New("no eligible embeddings; import papers and run make embeddings first")
	}
	if err := pool.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&result.PgvectorVersion); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `SELECT reloptions FROM pg_class WHERE oid = 'public.papers_embedding_hnsw_cosine_idx'::regclass`).
		Scan(&result.BuildOptions); err != nil {
		return fmt.Errorf("migration 004 HNSW index is required: %w", err)
	}
	missingIndexPlan := false
	for _, q := range fixture.Queries {
		vector, err := loadQuery(filepath.Join(*queryDir, q.File))
		if err != nil {
			return fmt.Errorf("%s: %w", q.Name, err)
		}
		literal, _ := search.VectorLiteral(vector)
		for _, k := range []int{10, 20} {
			row := measurement{Query: q.Name, K: k}
			exact, exactMS, err := timed(ctx, *repeats, func() ([]search.Result, error) {
				return search.Exact(ctx, pool, vector, k)
			})
			if err != nil {
				return err
			}
			hnsw, hnswMS, err := timed(ctx, *repeats, func() ([]search.Result, error) {
				return search.HNSW(ctx, pool, vector, k, *efSearch)
			})
			if err != nil {
				return err
			}
			row.ExactIDs, row.HNSWIDs = ids(exact), ids(hnsw)
			row.ExactMedianMS, row.HNSWMedianMS = exactMS, hnswMS
			if row.Recall, _ = search.RecallAtK(exact, hnsw, k); len(exact) == 0 {
				return fmt.Errorf("%s: no exact results", q.Name)
			}
			row.ExactPlan, err = explain(ctx, pool, search.ExactSQL,
				[]any{literal, search.ModelID, search.ModelRevision, search.TextVersion, k}, 0)
			if err != nil {
				return err
			}
			if containsIndex(row.ExactPlan, search.HNSWIndexName) {
				return errors.New("exact plan unexpectedly uses the HNSW index")
			}
			row.HNSWPlan, err = explain(ctx, pool, search.HNSWSQL, []any{literal, k}, *efSearch)
			if err != nil {
				return err
			}
			row.HNSWIndexUsed = containsIndex(row.HNSWPlan, search.HNSWIndexName)
			missingIndexPlan = missingIndexPlan || !row.HNSWIndexUsed
			result.Measurements = append(result.Measurements, row)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	if *requireIndex && missingIndexPlan {
		return errors.New("HNSW index absent from at least one plan; inspect the JSON plan, query shape, ANALYZE statistics, and eligible row count in the report")
	}
	return nil
}

func loadQuery(path string) ([]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var input queryFile
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	if input.ModelID != search.ModelID || input.ModelRevision != search.ModelRevision || input.TextVersion != search.TextVersion {
		return nil, errors.New("query model or text version mismatch")
	}
	if _, err := search.VectorLiteral(input.Embedding); err != nil {
		return nil, err
	}
	return input.Embedding, nil
}

func ids(results []search.Result) []int64 {
	out := make([]int64, 0, len(results))
	for _, result := range results {
		out = append(out, result.ID)
	}
	return out
}

func timed(ctx context.Context, repeats int, query func() ([]search.Result, error)) ([]search.Result, float64, error) {
	times := make([]float64, 0, repeats)
	var first []search.Result
	for i := 0; i < repeats; i++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		start := time.Now()
		results, err := query()
		if err != nil {
			return nil, 0, err
		}
		times = append(times, float64(time.Since(start).Microseconds())/1000)
		if i == 0 {
			first = results
		}
	}
	slices.Sort(times)
	return first, times[len(times)/2], nil
}

func explain(ctx context.Context, pool *pgxpool.Pool, sql string, args []any, efSearch int) (json.RawMessage, error) {
	if efSearch == 0 {
		var raw string
		err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...).Scan(&raw)
		return json.RawMessage(raw), err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('hnsw.ef_search', $1, true)`, strconv.Itoa(efSearch)); err != nil {
		return nil, err
	}
	var raw string
	err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...).Scan(&raw)
	return json.RawMessage(raw), err
}

func containsIndex(raw json.RawMessage, index string) bool {
	var plans []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		return false
	}
	for _, plan := range plans {
		if hasIndex(plan.Plan, index) {
			return true
		}
	}
	return false
}

func hasIndex(node map[string]any, index string) bool {
	if node["Index Name"] == index {
		return true
	}
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		if subnode, ok := child.(map[string]any); ok && hasIndex(subnode, index) {
			return true
		}
	}
	return false
}
