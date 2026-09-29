package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Persist the whole run in a single transaction so a partial experiment is
// never visible as a complete run.
func persist(ctx context.Context, pool *pgxpool.Pool, r *report) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		options, err := json.Marshal(r.IndexBuildOptions)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.bench_runs (
			run_id, experiment_id, config_version, config_sha256, git_commit, git_dirty,
			postgres_version, pgvector_version, dataset_snapshot, dataset_sha256, paper_count,
			eligible_count, model_id, model_revision, text_version, query_set_version,
			query_set_sha256, seed, warmup_iterations, measured_iterations, index_name,
			index_build_options, index_build_ms, index_build_note, percentile_definition, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
			r.RunID, r.ExperimentID, r.ConfigVersion, r.ConfigSHA256, r.GitCommit, r.GitDirty,
			r.PostgresVersion, r.PgvectorVersion, r.DatasetSnapshot, r.DatasetSHA256, r.Papers,
			r.EligibleVectors, r.ModelID, r.ModelRevision, r.TextVersion, r.QuerySetVersion,
			r.QuerySetSHA256, r.Seed, r.WarmupIterations, r.MeasuredIterations, r.IndexName,
			options, r.IndexBuildMS, r.IndexBuildNote, r.Percentile, r.CreatedAt)
		if err != nil {
			return err
		}
		for _, q := range r.Queries {
			_, err := tx.Exec(ctx, `INSERT INTO public.bench_queries
				(run_id, query_id, query_sha256, k, method, search_parameters, recall_at_k,
				p50_ms, p95_ms, p99_ms, index_used, plan_json)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
				r.RunID, q.QueryID, q.QuerySHA256, q.K, q.Method, q.Parameters, q.Recall,
				q.P50MS, q.P95MS, q.P99MS, q.IndexUsed, q.Plan)
			if err != nil {
				return err
			}
			for _, s := range q.Samples {
				_, err = tx.Exec(ctx, `INSERT INTO public.bench_samples
					(run_id, query_id, k, method, iteration, latency_ms, recall_at_k, result_ids)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
					r.RunID, q.QueryID, q.K, q.Method, s.Iteration, s.LatencyMS, s.Recall, s.ResultIDs)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func export(outDir string, r *report) error {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(outDir, r.RunID+".json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(r)
	if err := file.Close(); writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return writeErr
	}

	csvFile, err := os.OpenFile(filepath.Join(outDir, r.RunID+".csv"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writer := csv.NewWriter(csvFile)
	writeErr = writer.Write([]string{"run_id", "experiment_id", "query_id", "k", "method", "iteration",
		"latency_ms", "recall_at_k", "p50_ms", "p95_ms", "p99_ms", "index_used", "ef_search", "created_at"})
	for _, q := range r.Queries {
		for _, s := range q.Samples {
			if writeErr != nil {
				break
			}
			ef := ""
			if q.Method == "hnsw" {
				var params struct {
					EFSearch int `json:"ef_search"`
				}
				if err := json.Unmarshal(q.Parameters, &params); err != nil {
					writeErr = err
					break
				}
				ef = strconv.Itoa(params.EFSearch)
			}
			writeErr = writer.Write([]string{r.RunID, r.ExperimentID, q.QueryID, strconv.Itoa(q.K), q.Method,
				strconv.Itoa(s.Iteration), fmt.Sprint(s.LatencyMS), fmt.Sprint(s.Recall), fmt.Sprint(q.P50MS),
				fmt.Sprint(q.P95MS), fmt.Sprint(q.P99MS), strconv.FormatBool(q.IndexUsed), ef, r.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")})
		}
	}
	writer.Flush()
	if writeErr == nil {
		writeErr = writer.Error()
	}
	if err := csvFile.Close(); writeErr == nil {
		writeErr = err
	}
	return writeErr
}
