package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/kirbysss369/SciGraphDB/internal/database"
	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
	"github.com/kirbysss369/SciGraphDB/internal/search"
)

const maxFileSize = 64 << 10

type vectorFile struct {
	ModelID       string    `json:"model_id"`
	ModelRevision string    `json:"model_revision"`
	TextVersion   string    `json:"text_version"`
	Embedding     []float64 `json:"embedding"`
}

func readVector(r io.Reader) ([]float64, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if err != nil || len(body) > maxFileSize {
		return nil, errors.New("query vector file is too large or unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var data vectorFile
	if err := decoder.Decode(&data); err != nil {
		return nil, errors.New("invalid query vector JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("expected one query vector object")
	}
	if data.ModelID != search.ModelID || data.ModelRevision != search.ModelRevision || data.TextVersion != search.TextVersion {
		return nil, errors.New("query vector model or preprocessing version does not match stored embeddings")
	}
	if _, err := search.VectorLiteral(data.Embedding); err != nil {
		return nil, err
	}
	return data.Embedding, nil
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("vector-search", flag.ContinueOnError)
	path := flags.String("vector-file", "", "offline JSON query vector")
	limit := flags.Int("limit", search.DefaultLimit, "number of results (1..100)")
	method := flags.String("method", "exact", "exact or hnsw")
	efSearch := flags.Int("ef-search", search.DefaultEFSearch, "HNSW query candidate list size (1..1000)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return errors.New("usage: go run ./cmd/vector-search --vector-file FILE [--limit 1..100] [--method exact|hnsw] [--ef-search 1..1000]")
	}
	if err := search.ValidateLimit(*limit); err != nil {
		return err
	}
	if *method != "exact" && *method != "hnsw" {
		return errors.New("method must be exact or hnsw")
	}
	if err := search.ValidateEFSearch(*efSearch); err != nil {
		return err
	}
	file, err := os.Open(*path)
	if err != nil {
		return fmt.Errorf("open vector file: %w", err)
	}
	defer file.Close()
	vector, err := readVector(file)
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.New(ctx, dsn, 3*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	var results []search.Result
	if *method == "hnsw" {
		results, err = search.HNSW(ctx, pool, vector, *limit, *efSearch)
	} else {
		results, err = search.Exact(ctx, pool, vector, *limit)
	}
	if err != nil {
		return fmt.Errorf("vector search: %w", err)
	}
	return json.NewEncoder(output).Encode(struct {
		Results []search.Result `json:"results"`
	}{Results: results})
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		slog.Error("vector search failed", "error", err)
		os.Exit(1)
	}
}
