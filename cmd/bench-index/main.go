// bench-index manages a research IVFFlat index after vectors have been loaded.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kirbysss369/SciGraphDB/internal/database"
	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
	"github.com/kirbysss369/SciGraphDB/internal/search"
)

const indexName = "papers_embedding_ivfflat_cosine_idx"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bench-index:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("bench-index", flag.ContinueOnError)
	lists := flags.Int("lists", 3, "IVFFlat lists (2..1000, build only with at least 500 eligible vectors per list and 2000 total)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 || (*lists < 2 || *lists > 1000) {
		return errors.New("usage: bench-index [--lists 2..1000] build|status|drop")
	}
	action := flags.Arg(0)
	if action != "build" && action != "status" && action != "drop" {
		return errors.New("action must be build, status or drop")
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
	if action == "build" {
		return build(ctx, pool, *lists)
	}
	if action == "drop" {
		_, err := pool.Exec(ctx, `DROP INDEX IF EXISTS public.papers_embedding_ivfflat_cosine_idx`)
		if err == nil {
			fmt.Println(`{"index":"papers_embedding_ivfflat_cosine_idx","present":false}`)
		}
		return err
	}
	var options []string
	var valid bool
	err = pool.QueryRow(ctx, `SELECT c.reloptions, i.indisvalid FROM pg_class c
		JOIN pg_index i ON i.indexrelid = c.oid
		WHERE c.oid = to_regclass('public.papers_embedding_ivfflat_cosine_idx')`).Scan(&options, &valid)
	if err != nil {
		return fmt.Errorf("index absent (build after loading vectors): %w", err)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Name    string   `json:"index"`
		Options []string `json:"build_options"`
		Valid   bool     `json:"valid"`
	}{indexName, options, valid})
}

func build(ctx context.Context, pool *pgxpool.Pool, lists int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Keep the training corpus fixed until the index and its build record commit.
	if _, err := tx.Exec(ctx, `LOCK TABLE public.papers IN SHARE MODE`); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.papers_embedding_ivfflat_cosine_idx') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return errors.New("IVFFlat index already exists; drop it explicitly before rebuilding")
	}
	snapshot, err := search.SnapshotWithYears(ctx, tx)
	if err != nil {
		return err
	}
	minimum := int64(lists * 500)
	if minimum < 2000 {
		minimum = 2000
	}
	if snapshot.Eligible < minimum {
		return fmt.Errorf("need at least %d eligible vectors for %d lists (have %d); import and embed more first", minimum, lists, snapshot.Eligible)
	}
	// Lists is validated and interpolated only as a decimal integer; identifiers
	// and the partial predicate are fixed in source. No user SQL is accepted.
	query := fmt.Sprintf(`CREATE INDEX papers_embedding_ivfflat_cosine_idx ON public.papers
		USING ivfflat ((public.bench_ivf_vector(embedding)::public.vector(384)) public.vector_cosine_ops)
		WITH (lists = %d)
		WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
		AND embedding_model = 'sentence-transformers/all-MiniLM-L6-v2'
		AND embedding_revision = '1110a243fdf4706b3f48f1d95db1a4f5529b4d41'
		AND embedding_text_version = 'title-abstract-v1'`, lists)
	start := time.Now()
	if _, err := tx.Exec(ctx, query); err != nil {
		return err
	}
	buildMS := float64(time.Since(start).Nanoseconds()) / 1e6
	if _, err := tx.Exec(ctx, `ANALYZE public.papers`); err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	buildID := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	var postgres, pgvector string
	if err := tx.QueryRow(ctx, `SELECT current_setting('server_version'),
		(SELECT extversion FROM pg_extension WHERE extname='vector')`).Scan(&postgres, &pgvector); err != nil {
		return err
	}
	var maintenanceMem, parallelMaintenance, parallelWorkers string
	if err := tx.QueryRow(ctx, `SELECT current_setting('maintenance_work_mem'),
		current_setting('max_parallel_maintenance_workers'), current_setting('max_parallel_workers')`).
		Scan(&maintenanceMem, &parallelMaintenance, &parallelWorkers); err != nil {
		return err
	}
	buildSettings, err := json.Marshal(map[string]string{
		"maintenance_work_mem":             maintenanceMem,
		"max_parallel_maintenance_workers": parallelMaintenance,
		"max_parallel_workers":             parallelWorkers,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO public.bench_index_builds
		(build_id, index_name, lists, build_settings, eligible_count, dataset_sha256, build_ms,
		postgres_version, pgvector_version, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())`, buildID, indexName, lists,
		buildSettings, snapshot.Eligible, snapshot.SHA256, buildMS, postgres, pgvector)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		BuildID  string  `json:"build_id"`
		Index    string  `json:"index"`
		Lists    int     `json:"lists"`
		Eligible int64   `json:"eligible_count"`
		BuildMS  float64 `json:"build_ms"`
	}{buildID, indexName, lists, snapshot.Eligible, buildMS})
}
