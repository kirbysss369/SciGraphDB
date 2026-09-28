package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kirbysss369/SciGraphDB/internal/database"
	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
	"github.com/kirbysss369/SciGraphDB/internal/importer"
	"github.com/kirbysss369/SciGraphDB/internal/openalex"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stats, err := run(ctx, os.Args[1:], logger)
	fmt.Printf("fetched=%d inserted=%d updated=%d skipped=%d failed=%d citations_linked=%d\n",
		stats.Fetched, stats.Inserted, stats.Updated, stats.Skipped, stats.Failed, stats.CitationsLinked)
	if err != nil {
		logger.Error("import failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, logger *slog.Logger) (importer.Stats, error) {
	if err := devconfig.Load(); err != nil {
		return importer.Stats{}, err
	}
	flags := flag.NewFlagSet("importer", flag.ContinueOnError)
	search := flags.String("search", "", "words to search in OpenAlex works")
	fromYear := flags.Int("from-year", 0, "inclusive first publication year (optional)")
	toYear := flags.Int("to-year", 0, "inclusive last publication year (optional)")
	limit := flags.Int("limit", 10, "maximum number of works (1-10000)")
	if err := flags.Parse(args); err != nil {
		return importer.Stats{}, err
	}
	if flags.NArg() != 0 || *search == "" || *limit < 1 || *limit > 10000 ||
		(*fromYear != 0 && (*fromYear < 1 || *fromYear > 2100)) ||
		(*toYear != 0 && (*toYear < 1 || *toYear > 2100)) ||
		(*fromYear != 0 && *toYear != 0 && *fromYear > *toYear) {
		return importer.Stats{}, errors.New("usage: go run ./cmd/importer --search TEXT [--from-year YEAR] [--to-year YEAR] [--limit 1..10000]")
	}
	url, err := devconfig.DatabaseURL()
	if err != nil {
		return importer.Stats{}, err
	}
	connectTimeout := 3 * time.Second
	if text, ok := os.LookupEnv("DB_CONNECT_TIMEOUT"); ok {
		var err error
		connectTimeout, err = time.ParseDuration(text)
		if err != nil || connectTimeout <= 0 {
			return importer.Stats{}, errors.New("DB_CONNECT_TIMEOUT must be a positive duration")
		}
	}
	cfg, err := openalex.LoadConfig()
	if err != nil {
		return importer.Stats{}, err
	}
	if cfg.APIKey == "" && *limit > 10 {
		return importer.Stats{}, errors.New("OPENALEX_API_KEY is required for imports larger than 10 works")
	}
	client, err := openalex.New(cfg, nil)
	if err != nil {
		return importer.Stats{}, err
	}
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	pool, err := database.New(connectCtx, url, connectTimeout)
	if err != nil {
		return importer.Stats{}, fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(connectCtx); err != nil {
		return importer.Stats{}, fmt.Errorf("connect to database: %w", err)
	}
	return importer.Run(ctx, client, pool, openalex.Query{
		Search: *search, FromYear: *fromYear, ToYear: *toYear, Limit: *limit,
	}, logger)
}
