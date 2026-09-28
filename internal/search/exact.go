package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	Dimension     = 384
	DefaultLimit  = 10
	MaxLimit      = 100
	ModelID       = "sentence-transformers/all-MiniLM-L6-v2"
	ModelRevision = "1110a243fdf4706b3f48f1d95db1a4f5529b4d41"
	TextVersion   = "title-abstract-v1"
)

// ExactSQL is the shared query for the HTTP API, CLI, and EXPLAIN baseline.
// There is intentionally no ANN index. The primary key resolves exact ties.
const ExactSQL = `SELECT id, openalex_id, title, publication_year,
	       embedding <=> $1::public.vector AS distance
	FROM public.papers
	WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
	  AND embedding_model = $2 AND embedding_revision = $3
	  AND embedding_text_version = $4
	ORDER BY distance ASC, id ASC LIMIT $5`

type Result struct {
	ID              int64   `json:"id"`
	OpenAlexID      string  `json:"openalex_id"`
	Title           string  `json:"title"`
	PublicationYear *int    `json:"publication_year"`
	Distance        float64 `json:"distance"`
}

// VectorLiteral validates the JSON numbers before a pgvector cast. Values
// outside float32, including vectors that round entirely to zero, are invalid.
func VectorLiteral(vector []float64) (string, error) {
	if len(vector) != Dimension {
		return "", fmt.Errorf("embedding must contain exactly %d numbers", Dimension)
	}
	var b strings.Builder
	b.Grow(Dimension * 12)
	b.WriteByte('[')
	nonzero := false
	for i, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat32 {
			return "", errors.New("embedding values must be finite float32 numbers")
		}
		converted := float32(value)
		if converted != 0 {
			nonzero = true
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(converted), 'g', -1, 32))
	}
	b.WriteByte(']')
	if !nonzero {
		return "", errors.New("embedding must be nonzero for cosine distance")
	}
	return b.String(), nil
}

func ValidateLimit(limit int) error {
	if limit < 1 || limit > MaxLimit {
		return fmt.Errorf("limit must be between 1 and %d", MaxLimit)
	}
	return nil
}

func Exact(ctx context.Context, pool *pgxpool.Pool, vector []float64, limit int) ([]Result, error) {
	if err := ValidateLimit(limit); err != nil {
		return nil, err
	}
	literal, err := VectorLiteral(vector)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, ExactSQL, literal, ModelID, ModelRevision, TextVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]Result, 0, limit)
	for rows.Next() {
		var paper Result
		if err := rows.Scan(&paper.ID, &paper.OpenAlexID, &paper.Title, &paper.PublicationYear, &paper.Distance); err != nil {
			return nil, err
		}
		results = append(results, paper)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
