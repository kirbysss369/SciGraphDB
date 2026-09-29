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

func persistFiltered(ctx context.Context, pool *pgxpool.Pool, r *filteredReport) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		hnsw, _ := json.Marshal(r.HNSWIndexOptions)
		ivf, _ := json.Marshal(r.IVFIndexOptions)
		skipped, _ := json.Marshal(r.Skipped)
		_, err := tx.Exec(ctx, `INSERT INTO public.bench_filtered_runs
			(run_id,experiment_id,config_version,config_sha256,git_commit,git_dirty,
			 postgres_version,pgvector_version,dataset_snapshot,dataset_sha256,paper_count,
			 eligible_count,model_id,model_revision,text_version,query_set_version,
			 query_set_sha256,seed,warmup_iterations,measured_iterations,hnsw_index_options,
			 ivfflat_build_id,ivfflat_index_options,ivfflat_build_settings,ivfflat_build_ms,plan_mode,skipped_filters,
			 percentile_definition,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)`,
			r.RunID, r.ExperimentID, r.ConfigVersion, r.ConfigSHA256, r.GitCommit, r.GitDirty,
			r.PostgresVersion, r.PgvectorVersion, r.DatasetSnapshot, r.DatasetSHA256, r.Papers,
			r.EligibleVectors, r.ModelID, r.ModelRevision, r.TextVersion, r.QuerySetVersion,
			r.QuerySetSHA256, r.Seed, r.WarmupIterations, r.MeasuredIterations, hnsw,
			r.IVFBuildID, ivf, r.IVFBuildSettings, r.IVFBuildMS, r.PlanMode, skipped, r.Percentile, r.CreatedAt)
		if err != nil {
			return err
		}
		for _, q := range r.Queries {
			_, err = tx.Exec(ctx, `INSERT INTO public.bench_filtered_queries
				(run_id,filter_id,filter_cutoff,target_selectivity_pct,matching_count,
				 actual_selectivity,query_id,query_sha256,k,method,search_parameters,
				 postgres_settings,recall_at_k,p50_ms,p95_ms,p99_ms,returned_count,
				 returned_count_min,returned_count_max,too_few_samples,plan_index_name,plan_json)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
				r.RunID, q.Filter.ID, q.Filter.Cutoff, q.Filter.TargetPct, q.Filter.Matching,
				q.Filter.Actual, q.QueryID, q.QuerySHA256, q.K, q.Method, q.Parameters,
				q.PostgresSettings, q.Recall, q.P50MS, q.P95MS, q.P99MS, q.ReturnedCount,
				q.ReturnedCountMin, q.ReturnedCountMax, q.TooFewSamples, q.PlanIndexName, q.Plan)
			if err != nil {
				return err
			}
			for _, s := range q.Samples {
				_, err = tx.Exec(ctx, `INSERT INTO public.bench_filtered_samples
					(run_id,filter_id,query_id,k,method,iteration,latency_ms,recall_at_k,returned_count,result_ids)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
					r.RunID, q.Filter.ID, q.QueryID, q.K, q.Method, s.Iteration, s.LatencyMS, s.Recall, s.ReturnedCount, s.ResultIDs)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func exportFiltered(outDir string, r *filteredReport) error {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(outDir, r.RunID+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
	file, err = os.OpenFile(filepath.Join(outDir, r.RunID+".csv"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writer := csv.NewWriter(file)
	writeErr = writer.Write([]string{"run_id", "filter_id", "target_selectivity_pct", "publication_year_gte",
		"matching_count", "actual_selectivity", "query_id", "k", "method", "iteration", "latency_ms",
		"recall_at_k", "returned_count", "p50_ms", "p95_ms", "p99_ms", "plan_index_name",
		"search_parameters", "postgres_settings", "ivfflat_build_ms", "created_at"})
	for _, q := range r.Queries {
		cutoff := ""
		if q.Filter.Cutoff != nil {
			cutoff = strconv.Itoa(*q.Filter.Cutoff)
		}
		for _, s := range q.Samples {
			if writeErr != nil {
				break
			}
			writeErr = writer.Write([]string{r.RunID, q.Filter.ID, strconv.Itoa(q.Filter.TargetPct), cutoff,
				strconv.FormatInt(q.Filter.Matching, 10), fmt.Sprint(q.Filter.Actual), q.QueryID,
				strconv.Itoa(q.K), q.Method, strconv.Itoa(s.Iteration), fmt.Sprint(s.LatencyMS),
				fmt.Sprint(s.Recall), strconv.Itoa(s.ReturnedCount), fmt.Sprint(q.P50MS), fmt.Sprint(q.P95MS),
				fmt.Sprint(q.P99MS), q.PlanIndexName, string(q.Parameters), string(q.PostgresSettings),
				fmt.Sprint(r.IVFBuildMS), r.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")})
		}
	}
	writer.Flush()
	if writeErr == nil {
		writeErr = writer.Error()
	}
	if err := file.Close(); writeErr == nil {
		writeErr = err
	}
	return writeErr
}
