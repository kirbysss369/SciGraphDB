//go:build integration

package importer

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
	"github.com/kirbysss369/SciGraphDB/internal/openalex"
)

// DATABASE_URL_TEST must refer to an empty disposable database; this test
// creates and rolls back all migrations and all imported rows.
func TestImportCycle(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("set DATABASE_URL_TEST to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	states, err := migrations.Status(ctx, conn)
	if err != nil || len(states) != 3 || states[0].Applied || states[1].Applied || states[2].Applied {
		t.Fatalf("integration test requires an unmigrated disposable database: states=%v err=%v", states, err)
	}
	if _, err := migrations.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for range 3 {
			if _, err := migrations.Down(cleanupCtx, conn); err != nil {
				t.Errorf("cleanup migrations: %v", err)
			}
		}
	}()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int
		switch r.URL.Query().Get("search") {
		case "first-ten":
			n = 10
		case "first-hundred":
			n = 100
		default:
			t.Errorf("unexpected fixture search: %q", r.URL.Query().Get("search"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("per_page") != fmt.Sprint(n) || r.URL.Query().Get("cursor") != "*" {
			t.Errorf("unexpected paging: %s", r.URL.RawQuery)
		}
		works := make([]map[string]any, 0, n)
		for i := 1; i <= n; i++ {
			id := fmt.Sprintf("W%03d", i)
			title := fmt.Sprintf("Paper %03d", i)
			if i == 1 && n == 100 {
				title = "Updated paper"
			}
			references := []string{}
			if i == 1 {
				references = []string{"W003", "W100"}
			} else if i == 2 {
				references = []string{"W005"}
			}
			topicName := "Science"
			if n == 100 {
				topicName = "Updated science"
			}
			topics := []map[string]any{{"id": "T1", "display_name": topicName, "score": 0.8}}
			if i == 100 {
				topics = append(topics, map[string]any{"id": "T2", "display_name": "Databases", "score": 0.9})
			}
			works = append(works, map[string]any{
				"id": id, "display_name": title, "publication_year": 2020,
				"cited_by_count": i, "topics": topics, "referenced_works": references,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"next_cursor": nil}, "results": works})
	}))
	defer server.Close()
	client, err := openalex.New(openalex.Config{BaseURL: server.URL, Timeout: time.Second}, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	first, err := Run(ctx, client, pool, openalex.Query{Search: "first-ten", Limit: 10}, nil)
	if err != nil || first.Fetched != 10 || first.Inserted != 10 || first.Updated != 0 || first.CitationsLinked != 2 {
		t.Fatalf("10 works: stats=%+v err=%v", first, err)
	}
	checkCount(t, ctx, pool, "papers", 10)
	checkCount(t, ctx, pool, "citations", 2)
	checkCount(t, ctx, pool, "pending_citations", 1)

	second, err := Run(ctx, client, pool, openalex.Query{Search: "first-hundred", Limit: 100}, nil)
	if err != nil || second.Fetched != 100 || second.Inserted != 90 || second.Updated != 10 || second.CitationsLinked != 3 {
		t.Fatalf("100 works: stats=%+v err=%v", second, err)
	}
	checkCount(t, ctx, pool, "papers", 100)
	checkCount(t, ctx, pool, "topics", 2)
	checkCount(t, ctx, pool, "paper_topics", 101)
	checkCount(t, ctx, pool, "citations", 3)
	checkCount(t, ctx, pool, "pending_citations", 0)

	third, err := Run(ctx, client, pool, openalex.Query{Search: "first-hundred", Limit: 100}, nil)
	if err != nil || third.Inserted != 0 || third.Updated != 100 || third.Skipped != 0 || third.Failed != 0 {
		t.Fatalf("repeat import: stats=%+v err=%v", third, err)
	}
	checkCount(t, ctx, pool, "papers", 100)
	checkCount(t, ctx, pool, "paper_topics", 101)
	checkCount(t, ctx, pool, "citations", 3)
	checkCount(t, ctx, pool, "pending_citations", 0)

	var title, topicName, source string
	var referenceCount int
	if err := pool.QueryRow(ctx, `SELECT title, metadata->>'source', (metadata->>'reference_count')::integer
		FROM public.papers WHERE openalex_id = 'W001'`).Scan(&title, &source, &referenceCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT name FROM public.topics WHERE openalex_id = 'T1'`).Scan(&topicName); err != nil {
		t.Fatal(err)
	}
	if title != "Updated paper" || topicName != "Updated science" || source != "openalex" || referenceCount != 2 {
		t.Fatalf("upsert did not refresh metadata: title=%q topic=%q source=%q refs=%d", title, topicName, source, referenceCount)
	}
	var directed bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM public.citations c
		JOIN public.papers citing ON citing.id = c.citing_paper_id
		JOIN public.papers cited ON cited.id = c.cited_paper_id
		WHERE citing.openalex_id = 'W001' AND cited.openalex_id = 'W100')`).Scan(&directed); err != nil || !directed {
		t.Fatalf("late reference not linked in citation direction: found=%v err=%v", directed, err)
	}
}

func checkCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.`+table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("%s count=%d, want %d", table, got, want)
	}
}
