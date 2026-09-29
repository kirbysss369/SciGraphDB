package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type CorpusSnapshot struct {
	Papers   int64  `json:"paper_count"`
	Eligible int64  `json:"eligible_count"`
	SHA256   string `json:"dataset_sha256"`
}

// Filtered experiments include publication_year in the fingerprint. PostgreSQL
// sends only per-vector hashes, never the raw vectors, to the experiment runner.
type snapshotDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func SnapshotWithYears(ctx context.Context, pool snapshotDB) (CorpusSnapshot, error) {
	var out CorpusSnapshot
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.papers`).Scan(&out.Papers); err != nil {
		return out, err
	}
	rows, err := pool.Query(ctx, `SELECT id, openalex_id, publication_year, embedding_text_sha256,
		md5(embedding::text) FROM public.papers
		WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
		AND embedding_model = $1 AND embedding_revision = $2 AND embedding_text_version = $3
		ORDER BY id`, ModelID, ModelRevision, TextVersion)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var id int64
		var openalex, sourceHash, vectorHash string
		var year *int
		if err := rows.Scan(&id, &openalex, &year, &sourceHash, &vectorHash); err != nil {
			return out, err
		}
		fmt.Fprintf(h, "%d:%d:%s:%v:%s:%s\n", id, len(openalex), openalex, yearValue(year), sourceHash, vectorHash)
		out.Eligible++
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	fmt.Fprintf(h, "total=%d;eligible=%d", out.Papers, out.Eligible)
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	return out, nil
}

func yearValue(year *int) string {
	if year == nil {
		return "null"
	}
	return fmt.Sprint(*year)
}
