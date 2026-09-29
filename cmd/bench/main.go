// bench runs a versioned fixed-query exact/HNSW experiment and persists it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/kirbysss369/SciGraphDB/internal/database"
	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("bench", flag.ContinueOnError)
	configPath := flags.String("config", "experiments/configs/hnsw-cosine-v1.json", "versioned JSON experiment config")
	outDir := flags.String("out-dir", "experiments/results", "directory for JSON and CSV exports")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: bench [--config FILE] [--out-dir DIR]")
	}
	cfg, configSHA, err := readConfig(*configPath)
	if err != nil {
		return err
	}
	fixture, fixtureSHA, queries, err := loadQueries(cfg.QuerySet)
	if err != nil {
		return err
	}
	if err := devconfig.Load(); err != nil {
		return err
	}
	dsn, err := devconfig.DatabaseURL()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pool, err := database.New(ctx, dsn, 3*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	report, err := execute(ctx, pool, cfg, configSHA, fixture.Version, fixtureSHA, queries)
	if err != nil {
		return err
	}
	if err := persist(ctx, pool, report); err != nil {
		return err
	}
	if err := export(*outDir, report); err != nil {
		return fmt.Errorf("run %s stored but export failed: %w", report.RunID, err)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		RunID      string `json:"run_id"`
		QueryCount int    `json:"query_count"`
		OutputDir  string `json:"output_dir"`
		IndexUsed  bool   `json:"hnsw_index_used_for_all_queries"`
	}{report.RunID, len(report.Queries), *outDir, allIndexed(report.Queries)})
}
