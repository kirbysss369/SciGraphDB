package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kirbysss369/SciGraphDB/internal/openalex"
)

const batchSize = 100

type Fetcher interface {
	SearchWorks(context.Context, openalex.Query) ([]openalex.Work, error)
}

type Stats struct {
	Fetched         int
	Inserted        int
	Updated         int
	Skipped         int
	Failed          int
	CitationsLinked int
}

type paper struct {
	work     openalex.Work
	metadata string
}

// Run fetches matching works and commits each bounded batch atomically. On a
// failed batch it returns the counts for previously committed batches only.
func Run(ctx context.Context, source Fetcher, pool *pgxpool.Pool, query openalex.Query, logger *slog.Logger) (Stats, error) {
	var stats Stats
	var ready bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.pending_citations') IS NOT NULL`).Scan(&ready); err != nil {
		return stats, fmt.Errorf("check schema: %w", err)
	}
	if !ready {
		return stats, errors.New("pending_citations table missing: run make migrate")
	}
	works, err := source.SearchWorks(ctx, query)
	if err != nil {
		return stats, fmt.Errorf("fetch OpenAlex works: %w", err)
	}
	stats.Fetched = len(works)

	seen := make(map[string]bool, len(works))
	prepared := make([]paper, 0, len(works))
	for _, work := range works {
		if seen[work.ID] {
			stats.Skipped++
			continue
		}
		seen[work.ID] = true
		item, ok := prepare(work)
		if !ok {
			stats.Skipped++
			continue
		}
		prepared = append(prepared, item)
	}

	for offset := 0; offset < len(prepared); offset += batchSize {
		end := min(offset+batchSize, len(prepared))
		batch := prepared[offset:end]
		result, err := importBatch(ctx, pool, batch)
		if err != nil {
			if ctx.Err() == nil {
				stats.Failed += len(batch)
			}
			return stats, fmt.Errorf("import batch %d-%d: %w", offset+1, end, err)
		}
		stats.Inserted += result.Inserted
		stats.Updated += result.Updated
		stats.CitationsLinked += result.CitationsLinked
		if logger != nil {
			logger.Info("import batch committed", "processed", end, "total", len(prepared), "inserted", stats.Inserted, "updated", stats.Updated, "citations_linked", stats.CitationsLinked)
		}
	}
	return stats, nil
}

func prepare(work openalex.Work) (paper, bool) {
	if strings.TrimSpace(work.ID) == "" || strings.TrimSpace(work.Title) == "" || work.CitedByCount < 0 ||
		(work.PublicationYear != nil && (*work.PublicationYear < 1 || *work.PublicationYear > 2100)) {
		return paper{}, false
	}
	for _, topic := range work.Topics {
		if strings.TrimSpace(topic.ID) == "" || strings.TrimSpace(topic.Name) == "" ||
			math.IsNaN(topic.Score) || math.IsInf(topic.Score, 0) || topic.Score < 0 || topic.Score > 1 {
			return paper{}, false
		}
	}
	meta, err := json.Marshal(struct {
		Source         string `json:"source"`
		ReferenceCount int    `json:"reference_count"`
	}{"openalex", len(work.ReferencedWorks)})
	if err != nil {
		return paper{}, false
	}
	return paper{work: work, metadata: string(meta)}, true
}

type batchResult struct {
	Inserted        int
	Updated         int
	CitationsLinked int
}

func importBatch(ctx context.Context, pool *pgxpool.Pool, items []paper) (batchResult, error) {
	var result batchResult
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Serializes importer processes so the inserted/updated counts reflect
		// the state before this batch; each batch is still independently durable.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(619267183)`); err != nil {
			return err
		}
		openalexIDs := make([]string, 0, len(items))
		for _, item := range items {
			openalexIDs = append(openalexIDs, item.work.ID)
		}
		rows, err := tx.Query(ctx, `SELECT openalex_id FROM public.papers WHERE openalex_id = ANY($1::text[])`, openalexIDs)
		if err != nil {
			return err
		}
		existing := make(map[string]bool)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			existing[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}

		// The paper conflict replaces the sourced fields, including metadata;
		// created_at and the local primary key remain intact.
		var writes pgx.Batch
		for _, item := range items {
			work := item.work
			var doi any
			if work.DOI != "" {
				doi = work.DOI
			}
			writes.Queue(`INSERT INTO public.papers (openalex_id, doi, title, abstract, publication_year, cited_by_count, metadata)
				VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
				ON CONFLICT (openalex_id) DO UPDATE SET doi = EXCLUDED.doi, title = EXCLUDED.title,
				abstract = EXCLUDED.abstract, publication_year = EXCLUDED.publication_year,
				cited_by_count = EXCLUDED.cited_by_count, metadata = EXCLUDED.metadata, updated_at = now()
				RETURNING id`, work.ID, doi, work.Title, work.Abstract, work.PublicationYear, work.CitedByCount, item.metadata)
		}
		paperIDs := make([]int64, len(items))
		if err := scanIDs(ctx, tx, &writes, paperIDs); err != nil {
			return err
		}
		for _, item := range items {
			if existing[item.work.ID] {
				result.Updated++
			} else {
				result.Inserted++
			}
		}

		topicNames := make(map[string]string)
		for _, item := range items {
			for _, topic := range item.work.Topics {
				topicNames[topic.ID] = topic.Name
			}
		}
		topicKeys := make([]string, 0, len(topicNames))
		for id := range topicNames {
			topicKeys = append(topicKeys, id)
		}
		sort.Strings(topicKeys)
		var topicWrites pgx.Batch
		for _, id := range topicKeys {
			// A topic conflict updates its name without changing its local ID.
			topicWrites.Queue(`INSERT INTO public.topics (openalex_id, name) VALUES ($1, $2)
				ON CONFLICT (openalex_id) DO UPDATE SET name = EXCLUDED.name RETURNING id`, id, topicNames[id])
		}
		topicIDs := make([]int64, len(topicKeys))
		if len(topicKeys) > 0 {
			if err := scanIDs(ctx, tx, &topicWrites, topicIDs); err != nil {
				return err
			}
		}
		topicByID := make(map[string]int64, len(topicKeys))
		for i, id := range topicKeys {
			topicByID[id] = topicIDs[i]
		}

		// The source's current topic and reference lists replace the former
		// outgoing relationships. Incoming citations remain untouched.
		for _, table := range []string{"paper_topics", "citations", "pending_citations"} {
			column := "citing_paper_id"
			if table == "paper_topics" {
				column = "paper_id"
			}
			if _, err := tx.Exec(ctx, `DELETE FROM public.`+table+` WHERE `+column+` = ANY($1::bigint[])`, paperIDs); err != nil {
				return err
			}
		}
		var relations pgx.Batch
		for i, item := range items {
			seenTopics := make(map[string]bool)
			for _, topic := range item.work.Topics {
				if seenTopics[topic.ID] {
					continue
				}
				seenTopics[topic.ID] = true
				// A repeated pair refreshes its score; no duplicate row is possible.
				relations.Queue(`INSERT INTO public.paper_topics (paper_id, topic_id, score) VALUES ($1, $2, $3)
					ON CONFLICT (paper_id, topic_id) DO UPDATE SET score = EXCLUDED.score`, paperIDs[i], topicByID[topic.ID], topic.Score)
			}
			seenRefs := make(map[string]bool)
			for _, ref := range item.work.ReferencedWorks {
				if strings.TrimSpace(ref) == "" || ref == item.work.ID || seenRefs[ref] {
					continue
				}
				seenRefs[ref] = true
				// An unresolved pair is kept once until its cited work arrives.
				relations.Queue(`INSERT INTO public.pending_citations (citing_paper_id, cited_openalex_id) VALUES ($1, $2)
					ON CONFLICT (citing_paper_id, cited_openalex_id) DO NOTHING`, paperIDs[i], ref)
			}
		}
		if err := execBatch(ctx, tx, &relations); err != nil {
			return err
		}

		// Existing destinations are linked now; references to newly imported
		// destinations also resolve older pending rows from earlier batches/runs.
		const candidates = `FROM public.pending_citations AS pending
			JOIN public.papers AS target ON target.openalex_id = pending.cited_openalex_id
			WHERE (pending.cited_openalex_id = ANY($1::text[]) OR pending.citing_paper_id = ANY($2::bigint[]))
			AND pending.citing_paper_id <> target.id`
		if err := tx.QueryRow(ctx, `WITH linked AS (
			INSERT INTO public.citations (citing_paper_id, cited_paper_id)
			SELECT pending.citing_paper_id, target.id `+candidates+`
			ON CONFLICT (citing_paper_id, cited_paper_id) DO NOTHING RETURNING 1
		) SELECT count(*) FROM linked`, openalexIDs, paperIDs).Scan(&result.CitationsLinked); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM public.pending_citations AS pending USING public.papers AS target
			WHERE target.openalex_id = pending.cited_openalex_id
			AND (pending.cited_openalex_id = ANY($1::text[]) OR pending.citing_paper_id = ANY($2::bigint[]))
			AND pending.citing_paper_id <> target.id`, openalexIDs, paperIDs); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return batchResult{}, err
	}
	return result, nil
}

func scanIDs(ctx context.Context, tx pgx.Tx, batch *pgx.Batch, ids []int64) error {
	results := tx.SendBatch(ctx, batch)
	for i := range ids {
		if err := results.QueryRow().Scan(&ids[i]); err != nil {
			results.Close()
			return err
		}
	}
	return results.Close()
}

func execBatch(ctx context.Context, tx pgx.Tx, batch *pgx.Batch) error {
	if batch.Len() == 0 {
		return nil
	}
	results := tx.SendBatch(ctx, batch)
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return err
		}
	}
	return results.Close()
}
