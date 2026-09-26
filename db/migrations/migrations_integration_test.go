//go:build integration

package migrations

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DATABASE_URL_TEST must point at a disposable database. This test rolls back
// the schema and data it creates, including the vector extension.
func TestMigrationCycleAndConstraints(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("set DATABASE_URL_TEST to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelClose()
		if err := conn.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()

	before, err := Status(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 || before[0].Applied || before[1].Applied {
		t.Fatal("integration test requires a database without applied migrations")
	}
	defer func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		for range 2 {
			if _, err := Down(cleanupCtx, conn); err != nil {
				t.Errorf("cleanup migration: %v", err)
			}
		}
	}()

	changed, err := Up(ctx, conn)
	if err != nil || len(changed) != 2 {
		t.Fatalf("first up: changed=%d err=%v", len(changed), err)
	}
	if changed, err := Up(ctx, conn); err != nil || len(changed) != 0 {
		t.Fatalf("repeated up: changed=%d err=%v", len(changed), err)
	}
	verifySchema(t, ctx, conn, true)
	verifyConstraints(t, ctx, conn)

	if _, err := Down(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var pending bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.pending_citations') IS NOT NULL`).Scan(&pending); err != nil || pending {
		t.Fatalf("pending citations not rolled back: exists=%v err=%v", pending, err)
	}
	if _, err := Down(ctx, conn); err != nil {
		t.Fatal(err)
	}
	verifySchema(t, ctx, conn, false)

	changed, err = Up(ctx, conn)
	if err != nil || len(changed) != 2 {
		t.Fatalf("second up: changed=%d err=%v", len(changed), err)
	}
	verifySchema(t, ctx, conn, true)
}

func verifySchema(t *testing.T, ctx context.Context, conn *pgx.Conn, expected bool) {
	t.Helper()
	for _, name := range []string{"papers", "topics", "paper_topics", "citations", "pending_citations"} {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != expected {
			t.Errorf("table %s exists=%v, want %v", name, exists, expected)
		}
	}
	var extension bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')`).Scan(&extension); err != nil {
		t.Fatal(err)
	}
	if extension != expected {
		t.Errorf("vector extension exists=%v, want %v", extension, expected)
	}
}

func verifyConstraints(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var first, second, topic int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.papers (openalex_id, title) VALUES ('W1', 'First') RETURNING id`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO public.papers (openalex_id, title) VALUES ('W2', 'Second') RETURNING id`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO public.topics (openalex_id, name) VALUES ('T1', 'Databases') RETURNING id`).Scan(&topic); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		code string
		sql  string
		args []any
	}{
		{"duplicate paper", "23505", `INSERT INTO public.papers (openalex_id, title) VALUES ('W1', 'Duplicate')`, nil},
		{"blank title", "23514", `INSERT INTO public.papers (openalex_id, title) VALUES ('W3', ' ')`, nil},
		{"negative citations", "23514", `INSERT INTO public.papers (openalex_id, title, cited_by_count) VALUES ('W3', 'Invalid', -1)`, nil},
		{"invalid year", "23514", `INSERT INTO public.papers (openalex_id, title, publication_year) VALUES ('W3', 'Invalid', 0)`, nil},
		{"invalid metadata", "23514", `INSERT INTO public.papers (openalex_id, title, metadata) VALUES ('W3', 'Invalid', '[]')`, nil},
		{"missing paper FK", "23503", `INSERT INTO public.paper_topics (paper_id, topic_id, score) VALUES (-1, $1, 0.5)`, []any{topic}},
		{"invalid score", "23514", `INSERT INTO public.paper_topics (paper_id, topic_id, score) VALUES ($1, $2, 1.5)`, []any{first, topic}},
		{"self citation", "23514", `INSERT INTO public.citations (citing_paper_id, cited_paper_id) VALUES ($1, $1)`, []any{first}},
		{"missing citation FK", "23503", `INSERT INTO public.citations (citing_paper_id, cited_paper_id) VALUES ($1, -1)`, []any{first}},
		{"missing pending FK", "23503", `INSERT INTO public.pending_citations (citing_paper_id, cited_openalex_id) VALUES (-1, 'W3')`, nil},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			_, err := conn.Exec(ctx, check.sql, check.args...)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != check.code {
				t.Fatalf("got %v, want PostgreSQL code %s", err, check.code)
			}
		})
	}
	if _, err := conn.Exec(ctx, `INSERT INTO public.paper_topics (paper_id, topic_id, score) VALUES ($1, $2, 0.8)`, first, topic); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO public.citations (citing_paper_id, cited_paper_id) VALUES ($1, $2)`, first, second); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO public.pending_citations (citing_paper_id, cited_openalex_id) VALUES ($1, 'W3')`, first); err != nil {
		t.Fatal(err)
	}
}
