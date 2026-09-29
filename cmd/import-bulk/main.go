// import-bulk streams OpenAlex filter results into PostgreSQL with an atomic
// database checkpoint for every page. It can resume after interruption.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kirbysss369/SciGraphDB/internal/database"
	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
	"github.com/kirbysss369/SciGraphDB/internal/importer"
	"github.com/kirbysss369/SciGraphDB/internal/openalex"
)

var jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type progress struct {
	Cursor          string
	Target          int
	Fetched         int
	Inserted        int
	Updated         int
	Skipped         int
	CitationsLinked int
	Finished        bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bulk import:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("import-bulk", flag.ContinueOnError)
	job := flags.String("job", "", "stable import job identifier")
	filter := flags.String("filter", "", "OpenAlex works filter, e.g. type:article,has_abstract:true")
	limit := flags.Int("limit", 200000, "target number of fetched works (1..500000); can be increased on resume")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !jobIDPattern.MatchString(*job) ||
		strings.TrimSpace(*filter) == "" || len(*filter) > 1024 || strings.ContainsAny(*filter, "\r\n") ||
		*limit < 1 || *limit > 500000 {
		return errors.New("usage: import-bulk --job ID --filter OPENALEX_FILTER --limit 1..500000")
	}
	if err := devconfig.Load(); err != nil {
		return err
	}
	dsn, err := devconfig.DatabaseURL()
	if err != nil {
		return err
	}
	cfg, err := openalex.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.APIKey == "" {
		return errors.New("OPENALEX_API_KEY is required for bulk imports")
	}
	client, err := openalex.New(cfg, nil)
	if err != nil {
		return err
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pool, err := database.New(connectCtx, dsn, 5*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(connectCtx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	state, err := loadProgress(ctx, pool, *job, *filter, *limit)
	if err != nil {
		return err
	}
	if !state.Finished && state.Fetched < state.Target {
		err = client.StreamFiltered(ctx, *filter, state.Cursor, state.Target-state.Fetched,
			func(works []openalex.Work, next string, finished bool) error {
				stats, err := importer.ImportPage(ctx, pool, works, func(ctx context.Context, tx pgx.Tx, page importer.Stats) error {
					result, err := tx.Exec(ctx, `UPDATE public.openalex_bulk_imports SET
						next_cursor=$4, finished=$5, fetched_count=fetched_count+$6,
						inserted_count=inserted_count+$7, updated_count=updated_count+$8,
						skipped_count=skipped_count+$9, citations_linked=citations_linked+$10,
						updated_at=now()
						WHERE job_id=$1 AND next_cursor=$2 AND fetched_count=$3 AND NOT finished`,
						*job, state.Cursor, state.Fetched, next, finished, page.Fetched,
						page.Inserted, page.Updated, page.Skipped, page.CitationsLinked)
					if err != nil {
						return err
					}
					if result.RowsAffected() != 1 {
						return errors.New("import checkpoint changed; restart the job")
					}
					return nil
				})
				if err != nil {
					return err
				}
				state.Cursor, state.Finished = next, finished
				state.Fetched += stats.Fetched
				state.Inserted += stats.Inserted
				state.Updated += stats.Updated
				state.Skipped += stats.Skipped
				state.CitationsLinked += stats.CitationsLinked
				if state.Fetched%1000 == 0 || finished || state.Fetched == state.Target {
					fmt.Fprintf(os.Stderr, "job=%s fetched=%d/%d inserted=%d updated=%d skipped=%d\n",
						*job, state.Fetched, state.Target, state.Inserted, state.Updated, state.Skipped)
				}
				return nil
			})
	}
	fmt.Printf("job=%s fetched=%d/%d inserted=%d updated=%d skipped=%d citations_linked=%d source_exhausted=%t\n",
		*job, state.Fetched, state.Target, state.Inserted, state.Updated, state.Skipped,
		state.CitationsLinked, state.Finished)
	return err
}

func loadProgress(ctx context.Context, pool *pgxpool.Pool, job, filter string, target int) (progress, error) {
	var p progress
	if _, err := pool.Exec(ctx, `INSERT INTO public.openalex_bulk_imports (job_id, source_filter, target_count)
		VALUES ($1,$2,$3) ON CONFLICT (job_id) DO NOTHING`, job, filter, target); err != nil {
		return p, fmt.Errorf("initialize bulk job (run make migrate): %w", err)
	}
	var savedFilter string
	err := pool.QueryRow(ctx, `SELECT source_filter,target_count,next_cursor,fetched_count,inserted_count,
		updated_count,skipped_count,citations_linked,finished FROM public.openalex_bulk_imports WHERE job_id=$1`, job).
		Scan(&savedFilter, &p.Target, &p.Cursor, &p.Fetched, &p.Inserted, &p.Updated,
			&p.Skipped, &p.CitationsLinked, &p.Finished)
	if err != nil {
		return p, err
	}
	if savedFilter != filter || target < p.Target {
		return p, errors.New("job exists with a different filter or a larger target; use the original arguments")
	}
	if target > p.Target {
		result, err := pool.Exec(ctx, `UPDATE public.openalex_bulk_imports SET target_count=$2,updated_at=now()
			WHERE job_id=$1 AND target_count=$3 AND NOT finished`, job, target, p.Target)
		if err != nil {
			return p, err
		}
		if result.RowsAffected() != 1 {
			return p, errors.New("source exhausted or job changed; cannot extend this job")
		}
		p.Target = target
	}
	return p, nil
}
