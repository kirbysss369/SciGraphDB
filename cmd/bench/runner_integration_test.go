//go:build integration

package main

import (
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
	if err != nil || len(states) != 5 {
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
		for range 5 {
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
	var queries, samples, exactANN, rawVectors int
	if err := conn.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE method='exact' AND index_used),
		count(*) FILTER (WHERE plan_json::text LIKE '%0.9915619%') FROM public.bench_queries WHERE run_id=$1`, result.RunID).
		Scan(&queries, &exactANN, &rawVectors); err != nil {
		t.Fatal(err)
	}
	if queries != 12 || exactANN != 0 || rawVectors != 0 {
		t.Fatalf("queries=%d exactANN=%d rawVectors=%d", queries, exactANN, rawVectors)
	}
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
}
