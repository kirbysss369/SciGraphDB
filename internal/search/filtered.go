package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const IVFFlatIndexName = "papers_embedding_ivfflat_cosine_idx"

const filteredExactSQL = `WITH eligible AS MATERIALIZED (
	SELECT id, openalex_id, title, publication_year, embedding FROM public.papers
	WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
	AND embedding_model = $2 AND embedding_revision = $3 AND embedding_text_version = $4
	AND publication_year >= $5
)
SELECT id, openalex_id, title, publication_year, embedding <=> $1::public.vector AS distance
FROM eligible ORDER BY distance ASC, id ASC LIMIT $6`

// The filter stays inside the indexable query. PostgreSQL applies it after an
// ANN index scan; an iterative scan can request more candidates as needed.
const annFilteredSQL = `WITH candidates AS MATERIALIZED (
	SELECT id, openalex_id, title, publication_year,
	       embedding <=> $1::public.vector AS distance
	FROM public.papers
	WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
	AND embedding_model = 'sentence-transformers/all-MiniLM-L6-v2'
	AND embedding_revision = '1110a243fdf4706b3f48f1d95db1a4f5529b4d41'
	AND embedding_text_version = 'title-abstract-v1'
	AND publication_year >= $3
	ORDER BY embedding <=> $1::public.vector LIMIT $2
)
SELECT id, openalex_id, title, publication_year, distance FROM candidates
ORDER BY distance + 0 ASC, id ASC`

const ivfFilteredSQL = `WITH candidates AS MATERIALIZED (
	SELECT id, openalex_id, title, publication_year,
	       public.bench_ivf_vector(embedding)::public.vector(384) <=> $1::public.vector AS distance
	FROM public.papers
	WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
	AND embedding_model = 'sentence-transformers/all-MiniLM-L6-v2'
	AND embedding_revision = '1110a243fdf4706b3f48f1d95db1a4f5529b4d41'
	AND embedding_text_version = 'title-abstract-v1'
	AND publication_year >= $3
	ORDER BY public.bench_ivf_vector(embedding)::public.vector(384) <=> $1::public.vector LIMIT $2
)
SELECT id, openalex_id, title, publication_year, distance FROM candidates
ORDER BY distance + 0 ASC, id ASC`

const ivfUnfilteredSQL = `WITH candidates AS MATERIALIZED (
	SELECT id, openalex_id, title, publication_year,
	       public.bench_ivf_vector(embedding)::public.vector(384) <=> $1::public.vector AS distance
	FROM public.papers
	WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
	AND embedding_model = 'sentence-transformers/all-MiniLM-L6-v2'
	AND embedding_revision = '1110a243fdf4706b3f48f1d95db1a4f5529b4d41'
	AND embedding_text_version = 'title-abstract-v1'
	ORDER BY public.bench_ivf_vector(embedding)::public.vector(384) <=> $1::public.vector LIMIT $2
)
SELECT id, openalex_id, title, publication_year, distance FROM candidates
ORDER BY distance + 0 ASC, id ASC`

const annUnfilteredSQL = `WITH candidates AS MATERIALIZED (
	SELECT id, openalex_id, title, publication_year,
	       embedding <=> $1::public.vector AS distance
	FROM public.papers
	WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
	AND embedding_model = 'sentence-transformers/all-MiniLM-L6-v2'
	AND embedding_revision = '1110a243fdf4706b3f48f1d95db1a4f5529b4d41'
	AND embedding_text_version = 'title-abstract-v1'
	ORDER BY embedding <=> $1::public.vector LIMIT $2
)
SELECT id, openalex_id, title, publication_year, distance FROM candidates
ORDER BY distance + 0 ASC, id ASC`

type FilterOptions struct {
	EFSearch      int
	Probes        int
	MaxProbes     int
	IterativeScan string // off, strict_order, relaxed_order
}

func (o FilterOptions) Validate() error {
	if err := ValidateEFSearch(o.EFSearch); err != nil {
		return err
	}
	if o.Probes < 1 || o.MaxProbes < o.Probes || o.MaxProbes > 1000 {
		return errors.New("invalid IVFFlat probes or max_probes")
	}
	if o.IterativeScan != "off" && o.IterativeScan != "strict_order" && o.IterativeScan != "relaxed_order" {
		return errors.New("invalid iterative scan mode")
	}
	return nil
}

func filteredStatement(method string, cutoff *int, literal string, k int) (string, []any, error) {
	if cutoff == nil {
		switch method {
		case "exact":
			return ExactSQL, []any{literal, ModelID, ModelRevision, TextVersion, k}, nil
		case "hnsw":
			return annUnfilteredSQL, []any{literal, k}, nil
		case "ivfflat":
			return ivfUnfilteredSQL, []any{literal, k}, nil
		}
	} else {
		switch method {
		case "exact":
			return filteredExactSQL, []any{literal, ModelID, ModelRevision, TextVersion, *cutoff, k}, nil
		case "hnsw":
			return annFilteredSQL, []any{literal, k, *cutoff}, nil
		case "ivfflat":
			return ivfFilteredSQL, []any{literal, k, *cutoff}, nil
		}
	}
	return "", nil, fmt.Errorf("unsupported search method %q", method)
}

// Filtered executes the selected SQL with transaction-local ANN settings. It
// uses a custom, parameter-aware plan for each filter cutoff; no index is forced.
func Filtered(ctx context.Context, pool *pgxpool.Pool, method string, vector []float64, k int, cutoff *int, opts FilterOptions) ([]Result, error) {
	if err := ValidateLimit(k); err != nil {
		return nil, err
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	literal, err := VectorLiteral(vector)
	if err != nil {
		return nil, err
	}
	sql, args, err := filteredStatement(method, cutoff, literal, k)
	if err != nil {
		return nil, err
	}
	if method == "exact" {
		rows, err := pool.Query(ctx, sql, append([]any{pgx.QueryExecModeExec}, args...)...)
		if err != nil {
			return nil, err
		}
		return collect(rows, k)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := setFilterSettings(ctx, tx, method, opts); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, append([]any{pgx.QueryExecModeExec}, args...)...)
	if err != nil {
		return nil, err
	}
	return collect(rows, k)
}

func setFilterSettings(ctx context.Context, tx pgx.Tx, method string, opts FilterOptions) error {
	values := map[string]string{}
	switch method {
	case "hnsw":
		values["hnsw.ef_search"] = strconv.Itoa(opts.EFSearch)
		values["hnsw.iterative_scan"] = opts.IterativeScan
	case "ivfflat":
		values["ivfflat.probes"] = strconv.Itoa(opts.Probes)
		values["ivfflat.max_probes"] = strconv.Itoa(opts.MaxProbes)
		values["ivfflat.iterative_scan"] = opts.IterativeScan
	default:
		return errors.New("unsupported ANN method")
	}
	for key, value := range values {
		if _, err := tx.Exec(ctx, `SELECT set_config($1,$2,true)`, key, value); err != nil {
			return err
		}
	}
	return nil
}

// FilteredPlan captures actual PostgreSQL settings and a plan outside the
// timed loop. The caller must redact query vectors before storing the plan.
func FilteredPlan(ctx context.Context, pool *pgxpool.Pool, method string, vector []float64, k int, cutoff *int, opts FilterOptions) (json.RawMessage, json.RawMessage, error) {
	literal, err := VectorLiteral(vector)
	if err != nil {
		return nil, nil, err
	}
	sql, args, err := filteredStatement(method, cutoff, literal, k)
	if err != nil {
		return nil, nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	if method != "exact" {
		if err := setFilterSettings(ctx, tx, method, opts); err != nil {
			return nil, nil, err
		}
	}
	const settingsSQL = `SELECT current_setting('work_mem'), current_setting('enable_seqscan'),
		current_setting('random_page_cost'), current_setting('effective_cache_size'),
		current_setting('plan_cache_mode'), current_setting('hnsw.ef_search',true),
		current_setting('hnsw.iterative_scan',true), current_setting('hnsw.max_scan_tuples',true),
		current_setting('ivfflat.probes',true), current_setting('ivfflat.iterative_scan',true),
		current_setting('ivfflat.max_probes',true)`
	var vals [11]*string
	if err := tx.QueryRow(ctx, settingsSQL).Scan(&vals[0], &vals[1], &vals[2], &vals[3], &vals[4],
		&vals[5], &vals[6], &vals[7], &vals[8], &vals[9], &vals[10]); err != nil {
		return nil, nil, err
	}
	keys := []string{"work_mem", "enable_seqscan", "random_page_cost", "effective_cache_size", "plan_cache_mode",
		"hnsw.ef_search", "hnsw.iterative_scan", "hnsw.max_scan_tuples", "ivfflat.probes", "ivfflat.iterative_scan", "ivfflat.max_probes"}
	settings := make(map[string]*string, len(keys)+1)
	for i, key := range keys {
		settings[key] = vals[i]
	}
	mode := "pgx.QueryExecModeExec (custom parameter-aware plan)"
	settings["query_execution_mode"] = &mode
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return nil, nil, err
	}
	var raw string
	if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql,
		append([]any{pgx.QueryExecModeExec}, args...)...).Scan(&raw); err != nil {
		return nil, nil, err
	}
	return json.RawMessage(raw), settingsJSON, nil
}
