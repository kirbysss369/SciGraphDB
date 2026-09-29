//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kirbysss369/SciGraphDB/db/migrations"
	"github.com/kirbysss369/SciGraphDB/internal/importer"
	"github.com/kirbysss369/SciGraphDB/internal/openalex"
)

func TestBulkCheckpointResume(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("set DATABASE_URL_TEST to an empty disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	states, err := migrations.Status(ctx, conn)
	if err != nil || len(states) != 7 {
		t.Fatalf("migration status: %v %v", states, err)
	}
	for _, state := range states {
		if state.Applied {
			t.Fatal("test needs an empty disposable database")
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
				t.Error(err)
			}
		}
	}()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = importer.ImportPage(ctx, pool, []openalex.Work{{ID: "W-rolled-back", Title: "Temporary research paper"}},
		func(context.Context, pgx.Tx, importer.Stats) error { return fmt.Errorf("checkpoint rejected") })
	if err == nil {
		t.Fatal("expected checkpoint failure")
	}
	var rolledBack int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.papers WHERE openalex_id='W-rolled-back'`).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("page committed without checkpoint: rows=%d err=%v", rolledBack, err)
	}
	pool.Close()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("filter") != "type:article,has_abstract:true" || r.URL.Query().Has("search") ||
			r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("unexpected API request: %s", r.URL.RawQuery)
		}
		if requests == 1 {
			if r.URL.Query().Get("cursor") != "*" || r.URL.Query().Get("per_page") != "100" {
				t.Errorf("first page: %s", r.URL.RawQuery)
			}
			works := make([]map[string]any, 100)
			for i := range works {
				works[i] = map[string]any{"id": fmt.Sprintf("W-bulk-%03d", i),
					"display_name": fmt.Sprintf("Research paper %03d", i), "publication_year": 2020 + i%5}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"next_cursor": "next"}, "results": works})
			return
		}
		if r.URL.Query().Get("cursor") != "next" || r.URL.Query().Get("per_page") != "1" {
			t.Errorf("resumed page: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"meta":{"next_cursor":null},"results":[{"id":"W-bulk-last","display_name":"Last research paper","publication_year":2025}]}`)
	}))
	defer server.Close()
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("OPENALEX_BASE_URL", server.URL)
	t.Setenv("OPENALEX_API_KEY", "fixture-key")
	args := []string{"--job", "fixture-bulk", "--filter", "type:article,has_abstract:true", "--limit", "100"}
	if err := run(ctx, args); err != nil {
		t.Fatal(err)
	}
	var count, fetched int
	var cursor string
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.papers),fetched_count,next_cursor
		FROM public.openalex_bulk_imports WHERE job_id='fixture-bulk'`).Scan(&count, &fetched, &cursor); err != nil ||
		count != 100 || fetched != 100 || cursor != "next" || requests != 1 {
		t.Fatalf("first checkpoint: papers=%d fetched=%d cursor=%q calls=%d err=%v", count, fetched, cursor, requests, err)
	}
	args[len(args)-1] = "101"
	if err := run(ctx, args); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, args); err != nil {
		t.Fatal(err)
	}
	var finished bool
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.papers),fetched_count,finished
		FROM public.openalex_bulk_imports WHERE job_id='fixture-bulk'`).Scan(&count, &fetched, &finished); err != nil ||
		count != 101 || fetched != 101 || !finished || requests != 2 {
		t.Fatalf("resume: papers=%d fetched=%d finished=%t calls=%d err=%v", count, fetched, finished, requests, err)
	}
	if err := run(ctx, []string{"--job", "fixture-bulk", "--filter", "type:book", "--limit", "101"}); err == nil {
		t.Fatal("resumed job with a different filter")
	}
}
