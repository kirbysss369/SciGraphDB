//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kirbysss369/SciGraphDB/db/migrations"
)

// DATABASE_URL_TEST must designate an empty disposable database. This test
// invokes the actual cmd/bench CLI and checks its atomic persistence/export.
func TestRunnerPersistenceSmoke(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("set DATABASE_URL_TEST to an empty disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	states, err := migrations.Status(ctx, conn)
	if err != nil || len(states) != 7 {
		t.Fatalf("migration status: %v, %v", states, err)
	}
	for _, state := range states {
		if state.Applied {
			t.Fatal("requires unmigrated disposable database")
		}
	}
	if _, err := migrations.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for range 7 {
			if _, err := migrations.Down(cleanup, conn); err != nil {
				t.Errorf("cleanup migration: %v", err)
			}
		}
	}()
	for i := 1; i <= 40; i++ {
		angle := float64(i) * 0.13
		vector := make([]string, 384)
		for j := range vector {
			vector[j] = "0"
		}
		vector[0] = fmt.Sprint(math.Cos(angle))
		vector[1] = fmt.Sprint(math.Sin(angle))
		_, err := conn.Exec(ctx, `INSERT INTO public.papers
			(openalex_id, title, embedding, embedding_model, embedding_revision,
			 embedding_text_version, embedding_text_sha256, embedded_at)
			VALUES ($1,$2,$3::public.vector,$4,$5,$6,$7,now())`,
			fmt.Sprintf("W-bench-%03d", i), "Benchmark fixture", "["+strings.Join(vector, ",")+"]",
			"sentence-transformers/all-MiniLM-L6-v2", "1110a243fdf4706b3f48f1d95db1a4f5529b4d41",
			"title-abstract-v1", strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
	}
	outDir := t.TempDir()
	command := exec.CommandContext(ctx, "go", "run", "./cmd/bench", "--config", "experiments/configs/smoke-v1.json", "--out-dir", outDir)
	command.Dir = filepath.Join("..", "..")
	command.Env = append(os.Environ(), "DATABASE_URL="+dsn)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("cmd/bench smoke: %v: %s", err, output)
	}
	var result struct {
		RunID      string `json:"run_id"`
		QueryCount int    `json:"query_count"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.QueryCount != 12 || result.RunID == "" {
		t.Fatalf("summary: %+v", result)
	}
	var queries, samples, exactANN int
	if err := conn.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE method='exact' AND index_used)
		FROM public.bench_queries WHERE run_id=$1`, result.RunID).
		Scan(&queries, &exactANN); err != nil {
		t.Fatal(err)
	}
	if queries != 12 || exactANN != 0 {
		t.Fatalf("queries=%d exactANN=%d", queries, exactANN)
	}
	plans, err := conn.Query(ctx, `SELECT plan_json FROM public.bench_queries WHERE run_id=$1`, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for plans.Next() {
		var raw json.RawMessage
		if err := plans.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		canonical, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		redacted, err := redactPlan(raw)
		if err != nil || !bytes.Equal(canonical, redacted) {
			t.Fatal("persisted plan contains a raw query vector")
		}
	}
	if err := plans.Err(); err != nil {
		t.Fatal(err)
	}
	plans.Close()
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.bench_samples WHERE run_id=$1`, result.RunID).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if samples != 36 {
		t.Fatalf("expected 36 measured samples, got %d", samples)
	}
	var buildMS *float64
	var seed int64
	var datasetSHA string
	if err := conn.QueryRow(ctx, `SELECT index_build_ms, seed, dataset_sha256 FROM public.bench_runs WHERE run_id=$1`, result.RunID).
		Scan(&buildMS, &seed, &datasetSHA); err != nil {
		t.Fatal(err)
	}
	if buildMS != nil || seed != 42 || len(datasetSHA) != 64 {
		t.Fatalf("run metadata: build=%v seed=%d sha=%s", buildMS, seed, datasetSHA)
	}
	data, err := os.ReadFile(filepath.Join(outDir, result.RunID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var exported report
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.RunID != result.RunID || len(exported.Queries) != queries || len(exported.Queries[0].Samples) != 3 {
		t.Fatal("JSON export differs from stored run")
	}
	csvFile, err := os.Open(filepath.Join(outDir, result.RunID+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer csvFile.Close()
	csvRows, err := csv.NewReader(csvFile).ReadAll()
	if err != nil || len(csvRows) != 37 {
		t.Fatalf("CSV rows=%d err=%v", len(csvRows), err)
	}
	indexCmd := exec.CommandContext(ctx, "go", "run", "./cmd/bench-index", "--lists", "3", "build")
	indexCmd.Dir = filepath.Join("..", "..")
	indexCmd.Env = append(os.Environ(), "DATABASE_URL="+dsn)
	if output, err := indexCmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "need at least") {
		t.Fatalf("small corpus accepted for IVFFlat: %v %s", err, output)
	}
	_, err = conn.Exec(ctx, `INSERT INTO public.papers
		(openalex_id,title,publication_year,embedding,embedding_model,embedding_revision,
		 embedding_text_version,embedding_text_sha256,embedded_at)
		SELECT 'W-filter-'||n, 'Filtered fixture',
		CASE WHEN n<=1500 THEN 2000 WHEN n<=2250 THEN 2001
		     WHEN n<=2700 THEN 2002 WHEN n<=2850 THEN 2003
		     WHEN n<=2970 THEN 2004 ELSE 2005 END,
		('[' || ((n%101)+1)::text || ',' || (((n*17)%103)+1)::text || ',' || repeat('0,',381) || '1]')::public.vector,
		$1,$2,$3,repeat('b',64),now()
		FROM generate_series(1,3000) AS n`,
		"sentence-transformers/all-MiniLM-L6-v2", "1110a243fdf4706b3f48f1d95db1a4f5529b4d41", "title-abstract-v1")
	if err != nil {
		t.Fatal(err)
	}
	indexCmd = exec.CommandContext(ctx, "go", "run", "./cmd/bench-index", "--lists", "3", "build")
	indexCmd.Dir = filepath.Join("..", "..")
	indexCmd.Env = append(os.Environ(), "DATABASE_URL="+dsn)
	if output, err := indexCmd.CombinedOutput(); err != nil {
		t.Fatalf("IVFFlat build: %v %s", err, output)
	}
	filteredCmd := exec.CommandContext(ctx, "go", "run", "./cmd/bench", "--config", "experiments/configs/filtered-smoke-v2.json", "--out-dir", outDir)
	filteredCmd.Dir = filepath.Join("..", "..")
	filteredCmd.Env = append(os.Environ(), "DATABASE_URL="+dsn)
	filteredOutput, err := filteredCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("filtered cmd/bench: %v %s", err, filteredOutput)
	}
	var filteredSummary struct {
		RunID      string `json:"run_id"`
		QueryCount int    `json:"query_count"`
		Skipped    int    `json:"skipped_filters"`
	}
	if err := json.Unmarshal(filteredOutput, &filteredSummary); err != nil {
		t.Fatal(err)
	}
	if filteredSummary.QueryCount != 108 || filteredSummary.Skipped != 0 {
		t.Fatalf("filtered summary: %+v", filteredSummary)
	}
	filteredJSON, err := os.ReadFile(filepath.Join(outDir, filteredSummary.RunID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("FILTERED_BENCH_REPORT_PATH"); path != "" {
		if err := os.WriteFile(path, filteredJSON, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var filtered filteredReport
	if err := json.Unmarshal(filteredJSON, &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.EligibleVectors != 3040 || len(filtered.Queries) != 108 || filtered.IVFBuildMS <= 0 {
		t.Fatalf("filtered run metadata: eligible=%d queries=%d build_ms=%f", filtered.EligibleVectors, len(filtered.Queries), filtered.IVFBuildMS)
	}
	var hnswPlans, ivfPlans int
	for _, q := range filtered.Queries {
		if len(q.Samples) != 3 || q.Filter.Matching <= 0 || q.Filter.Actual != float64(q.Filter.Matching)/3040 {
			t.Fatalf("invalid sample or selectivity: %+v", q.Filter)
		}
		if filtered.PlanMode != "planner" || len(q.PostgresSettings) == 0 || q.P50MS < 0 || q.P95MS < q.P50MS || q.P99MS < q.P95MS {
			t.Fatalf("invalid plan settings or latency summary: %+v", q)
		}
		var decoded any
		if err := json.Unmarshal(q.Plan, &decoded); err != nil {
			t.Fatal(err)
		}
		canonical, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		redacted, err := redactPlan(q.Plan)
		if err != nil || !bytes.Equal(canonical, redacted) {
			t.Fatal("filtered plan contains a raw query vector")
		}
		if q.Method == "hnsw" && strings.Contains(q.PlanIndexName, "papers_embedding_hnsw_cosine_idx") {
			hnswPlans++
		}
		if q.Method == "ivfflat" && strings.Contains(q.PlanIndexName, "papers_embedding_ivfflat_cosine_idx") {
			ivfPlans++
		}
		for _, s := range q.Samples {
			if s.ReturnedCount < len(s.ResultIDs) || (s.ReturnedCount < q.K && s.ReturnedCount < min(q.K, int(q.Filter.Matching)) && q.TooFewSamples == 0) {
				t.Fatal("short ANN result was not accounted for")
			}
		}
	}
	if hnswPlans == 0 || ivfPlans == 0 {
		t.Fatalf("expected both ANN indexes on the 3040-row fixture: hnsw=%d ivfflat=%d", hnswPlans, ivfPlans)
	}
	var stored, storedSamples int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.bench_filtered_queries WHERE run_id=$1`, filteredSummary.RunID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.bench_filtered_samples WHERE run_id=$1`, filteredSummary.RunID).Scan(&storedSamples); err != nil {
		t.Fatal(err)
	}
	if stored != 108 || storedSamples != 324 {
		t.Fatalf("stored rows: queries=%d samples=%d", stored, storedSamples)
	}
	indexCmd = exec.CommandContext(ctx, "go", "run", "./cmd/bench-index", "drop")
	indexCmd.Dir = filepath.Join("..", "..")
	indexCmd.Env = append(os.Environ(), "DATABASE_URL="+dsn)
	if output, err := indexCmd.CombinedOutput(); err != nil {
		t.Fatalf("IVFFlat drop: %v %s", err, output)
	}
}
