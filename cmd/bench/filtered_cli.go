package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kirbysss369/SciGraphDB/internal/database"
	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
)

func runFilteredCLI(configPath, outDir string) error {
	cfg, cfgSHA, err := readFilteredConfig(configPath)
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
	r, err := executeFiltered(ctx, pool, cfg, cfgSHA, fixture.Version, fixtureSHA, queries)
	if err != nil {
		return err
	}
	if err := persistFiltered(ctx, pool, r); err != nil {
		return err
	}
	if err := exportFiltered(outDir, r); err != nil {
		return fmt.Errorf("run %s stored but export failed: %w", r.RunID, err)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		RunID          string `json:"run_id"`
		QueryCount     int    `json:"query_count"`
		SkippedFilters int    `json:"skipped_filters"`
		OutputDir      string `json:"output_dir"`
	}{r.RunID, len(r.Queries), len(r.Skipped), outDir})
}
