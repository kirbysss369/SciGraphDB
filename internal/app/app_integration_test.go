//go:build integration

package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kirbysss369/SciGraphDB/db/migrations"
	"github.com/kirbysss369/SciGraphDB/internal/config"
	"github.com/kirbysss369/SciGraphDB/internal/importer"
	"github.com/kirbysss369/SciGraphDB/internal/openalex"
)

// This test owns the schema in an empty disposable DATABASE_URL_TEST database.
func TestMigrateImportAndServe(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("set DATABASE_URL_TEST to an empty disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	states, err := migrations.Status(ctx, conn)
	if err != nil || len(states) != 3 || states[0].Applied || states[1].Applied || states[2].Applied {
		t.Fatalf("requires an unmigrated disposable database: states=%v err=%v", states, err)
	}
	if _, err := migrations.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for range 3 {
			if _, err := migrations.Down(cleanupCtx, conn); err != nil {
				t.Errorf("cleanup migration: %v", err)
			}
		}
	}()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/works" || r.URL.Query().Get("search") != "fixture" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]any{"next_cursor": nil},
			"results": []map[string]any{
				{"id": "W1", "display_name": "First", "publication_year": 2024, "cited_by_count": 1,
					"topics": []map[string]any{{"id": "T1", "display_name": "Science", "score": 0.9}}, "referenced_works": []string{"W2"}},
				{"id": "W2", "display_name": "Second", "publication_year": 2023, "cited_by_count": 0},
			},
		})
	}))
	defer fixture.Close()
	client, err := openalex.New(openalex.Config{BaseURL: fixture.URL, Timeout: time.Second}, fixture.Client())
	if err != nil {
		t.Fatal(err)
	}
	stats, err := importer.Run(ctx, client, pool, openalex.Query{Search: "fixture", Limit: 2}, nil)
	if err != nil || stats.Fetched != 2 || stats.Inserted != 2 || stats.CitationsLinked != 1 {
		t.Fatalf("import: stats=%+v err=%v", stats, err)
	}
	var papers, topics, citations int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM papers), (SELECT count(*) FROM topics), (SELECT count(*) FROM citations)`).Scan(&papers, &topics, &citations); err != nil {
		t.Fatal(err)
	}
	if papers != 2 || topics != 1 || citations != 1 {
		t.Fatalf("persisted papers=%d topics=%d citations=%d", papers, topics, citations)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	serverCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	served := make(chan error, 1)
	go func() {
		served <- Run(serverCtx, config.Config{
			HTTPAddr: addr, DatabaseURL: dsn,
			HTTPReadTimeout: 5 * time.Second, HTTPWriteTimeout: 5 * time.Second,
			HTTPIdleTimeout: 30 * time.Second, DBConnectTimeout: time.Second, DBPingTimeout: time.Second,
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	httpClient := &http.Client{Timeout: time.Second}
	for _, endpoint := range []string{"/healthz", "/readyz"} {
		var response *http.Response
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			response, err = httpClient.Get("http://" + addr + endpoint)
			if err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("GET %s: %v", endpoint, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d", endpoint, response.StatusCode)
		}
	}
	stopServer()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}
