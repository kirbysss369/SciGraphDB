//go:build integration

package app

import (
	"bytes"
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
	"github.com/kirbysss369/SciGraphDB/internal/search"
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
	if err != nil || len(states) != 7 || states[0].Applied || states[1].Applied || states[2].Applied || states[3].Applied || states[4].Applied || states[5].Applied || states[6].Applied {
		t.Fatalf("requires an unmigrated disposable database: states=%v err=%v", states, err)
	}
	if _, err := migrations.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for range 7 {
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
	queryVector := make([]float64, search.Dimension)
	queryVector[0] = 1
	literal, err := search.VectorLiteral(queryVector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.papers SET embedding = $1::public.vector,
		embedding_model = $2, embedding_revision = $3, embedding_text_version = $4,
		embedding_text_sha256 = repeat('a', 64), embedded_at = now()
		WHERE openalex_id = 'W1'`, literal, search.ModelID, search.ModelRevision, search.TextVersion); err != nil {
		t.Fatal(err)
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
	body, err := json.Marshal(map[string]any{"embedding": queryVector, "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	response, err := httpClient.Post("http://"+addr+"/api/v1/search/vector", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var found struct {
		Results []search.Result `json:"results"`
	}
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil || response.StatusCode != http.StatusOK || len(found.Results) != 1 || found.Results[0].OpenAlexID != "W1" || found.Results[0].Distance != 0 {
		t.Fatalf("vector search status=%d results=%+v err=%v", response.StatusCode, found.Results, err)
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
