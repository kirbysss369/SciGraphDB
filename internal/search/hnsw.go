package search

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	HNSWIndexName   = "papers_embedding_hnsw_cosine_idx"
	DefaultEFSearch = 80
	MaxEFSearch     = 1000
)

// Keep the constant model and text predicates identical to migration 004.
// The inner ORDER BY is an indexable cosine expression with a bounded LIMIT.
// The outer sort resolves ties among returned candidates; ANN can still omit
// candidates, including ties at the boundary.
const HNSWSQL = `WITH candidates AS MATERIALIZED (
	SELECT id, openalex_id, title, publication_year,
	       embedding <=> $1::public.vector AS distance
	FROM public.papers
	WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
	  AND embedding_model = 'sentence-transformers/all-MiniLM-L6-v2'
	  AND embedding_revision = '1110a243fdf4706b3f48f1d95db1a4f5529b4d41'
	  AND embedding_text_version = 'title-abstract-v1'
	ORDER BY embedding <=> $1::public.vector LIMIT $2
)
SELECT id, openalex_id, title, publication_year, distance
FROM candidates ORDER BY distance ASC, id ASC`

func ValidateEFSearch(value int) error {
	if value < 1 || value > MaxEFSearch {
		return fmt.Errorf("ef_search must be between 1 and %d", MaxEFSearch)
	}
	return nil
}

// HNSW sets ef_search for this transaction only, so pooled connections do
// not leak query settings to unrelated requests.
func HNSW(ctx context.Context, pool *pgxpool.Pool, vector []float64, limit, efSearch int) ([]Result, error) {
	if err := ValidateLimit(limit); err != nil {
		return nil, err
	}
	if err := ValidateEFSearch(efSearch); err != nil {
		return nil, err
	}
	literal, err := VectorLiteral(vector)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var indexExists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.papers_embedding_hnsw_cosine_idx') IS NOT NULL`).Scan(&indexExists); err != nil {
		return nil, err
	}
	if !indexExists {
		return nil, errors.New("HNSW index is missing; apply migration 004")
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('hnsw.ef_search', $1, true)`, strconv.Itoa(efSearch)); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, HNSWSQL, literal, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, limit)
}
