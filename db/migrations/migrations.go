package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

//go:embed *.sql
var scripts embed.FS

type Migration struct {
	Version  int
	Name     string
	UpFile   string
	DownFile string
}

type State struct {
	Version int
	Name    string
	Applied bool
}

var ordered = []Migration{
	{1, "initial_schema", "001_initial_schema.up.sql", "001_initial_schema.down.sql"},
	{2, "pending_citations", "002_pending_citations.up.sql", "002_pending_citations.down.sql"},
	{3, "paper_embeddings", "003_paper_embeddings.up.sql", "003_paper_embeddings.down.sql"},
	{4, "paper_hnsw", "004_paper_hnsw.up.sql", "004_paper_hnsw.down.sql"},
	{5, "bench_runs", "005_bench_runs.up.sql", "005_bench_runs.down.sql"},
}

const versionTable = `CREATE TABLE IF NOT EXISTS public.schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

const advisoryLockKey int64 = 619267182

type appliedRecord struct {
	name     string
	checksum string
}

// Up applies all pending migrations in one transaction.
func Up(ctx context.Context, conn *pgx.Conn) ([]Migration, error) {
	var changed []Migration
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if err := lockAndEnsure(ctx, tx); err != nil {
			return err
		}
		installed, err := applied(ctx, tx)
		if err != nil {
			return err
		}
		if err := validate(installed); err != nil {
			return err
		}
		for _, migration := range ordered {
			if _, ok := installed[migration.Version]; ok {
				continue
			}
			body, err := scripts.ReadFile(migration.UpFile)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
				return fmt.Errorf("apply %s: %w", migration.Name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO public.schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`, migration.Version, migration.Name, checksum(body)); err != nil {
				return err
			}
			changed = append(changed, migration)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// Down rolls back the latest applied migration.
func Down(ctx context.Context, conn *pgx.Conn) (*Migration, error) {
	var rolledBack *Migration
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if err := lockAndEnsure(ctx, tx); err != nil {
			return err
		}
		installed, err := applied(ctx, tx)
		if err != nil {
			return err
		}
		if err := validate(installed); err != nil {
			return err
		}
		for i := len(ordered) - 1; i >= 0; i-- {
			migration := ordered[i]
			if _, ok := installed[migration.Version]; !ok {
				continue
			}
			body, err := scripts.ReadFile(migration.DownFile)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
				return fmt.Errorf("roll back %s: %w", migration.Name, err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM public.schema_migrations WHERE version = $1`, migration.Version); err != nil {
				return err
			}
			rolledBack = &migration
			break
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rolledBack, nil
}

// Status reads applied versions without modifying the database.
func Status(ctx context.Context, conn *pgx.Conn) ([]State, error) {
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, err
	}
	installed := make(map[int]appliedRecord)
	if exists {
		var err error
		installed, err = applied(ctx, conn)
		if err != nil {
			return nil, err
		}
		if err := validate(installed); err != nil {
			return nil, err
		}
	}
	states := make([]State, 0, len(ordered))
	for _, migration := range ordered {
		_, ok := installed[migration.Version]
		states = append(states, State{migration.Version, migration.Name, ok})
	}
	return states, nil
}

type queryable interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func applied(ctx context.Context, db queryable) (map[int]appliedRecord, error) {
	rows, err := db.Query(ctx, `SELECT version, name, checksum FROM public.schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int]appliedRecord)
	for rows.Next() {
		var version int
		var record appliedRecord
		if err := rows.Scan(&version, &record.name, &record.checksum); err != nil {
			return nil, err
		}
		result[version] = record
	}
	return result, rows.Err()
}

func validate(installed map[int]appliedRecord) error {
	known := make(map[int]Migration, len(ordered))
	for _, migration := range ordered {
		known[migration.Version] = migration
	}
	versions := make([]int, 0, len(installed))
	for version := range installed {
		versions = append(versions, version)
	}
	sort.Ints(versions)
	for _, version := range versions {
		migration, ok := known[version]
		if !ok {
			return fmt.Errorf("unknown applied migration %d", version)
		}
		body, err := scripts.ReadFile(migration.UpFile)
		if err != nil {
			return err
		}
		record := installed[version]
		if record.name != migration.Name || record.checksum != checksum(body) {
			return fmt.Errorf("applied migration %d has changed", version)
		}
	}
	return nil
}

func checksum(body []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func lockAndEnsure(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockKey); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, versionTable); err != nil {
		return err
	}
	return nil
}
