# SciGraphDB

SciGraphDB is a research project for scientific paper retrieval and relation analysis. The repository currently contains a minimal Go API, PostgreSQL 18 with pgvector 0.8.6, and directories for future research work.

## Start locally

1. Copy the example configuration: `cp .env.example .env`.
2. Set a local password in `.env`.
3. Check the development tools: `make doctor`.
4. Start the database: `make up`.
5. Open `psql`: `make db-shell`.

Inside `psql`, enable and inspect the extension:

```sql
CREATE EXTENSION IF NOT EXISTS vector;
SELECT current_setting('server_version');
SELECT extversion FROM pg_extension WHERE extname = 'vector';
```

`make down` stops the service and keeps its data. `make db-reset` deletes the database volume and requires an explicit confirmation. The development setup for Ubuntu and Fedora is in [docs/development.md](docs/development.md).

## API

Start PostgreSQL with `make up`. In **terminal 1**, export a URL whose password matches `.env`, then leave the API running:

```bash
export DATABASE_URL='postgres://scigraph:<your-password>@127.0.0.1:5432/scigraph?sslmode=disable'
make run
```

While terminal 1 shows `HTTP server listening`, use **terminal 2**:

```bash
curl --max-time 5 http://127.0.0.1:8080/healthz
curl --max-time 5 http://127.0.0.1:8080/readyz
```

`make run` stays in the foreground. Pressing Ctrl+C stops the API, after which `curl` cannot connect; Make may print `Error 1` for the interrupted command. `GET /healthz` returns 200 while the process is running. `GET /readyz` returns 200 when PostgreSQL responds to a ping and 503 otherwise. `make fmt` and `make test` format and test the Go packages.

## Database schema

With `DATABASE_URL` exported and PostgreSQL running, use `make migrate` to create the tables and pgvector extension. `make migrate-status` shows applied versions. `make migrate-down` rolls back the latest migration and **deletes its data**. The SQL files are under `db/migrations/`; the runner is part of the Go project. See [the migration procedure](docs/development.md#migrations). Vector columns are not implemented yet.

## OpenAlex probe

The read-only works client can query OpenAlex without a database. For a small keyless check, run `go run ./cmd/openalex-probe --search 'graph databases' --limit 3`. Export `OPENALEX_API_KEY` for larger probes. The client reads `OPENALEX_BASE_URL` and `OPENALEX_TIMEOUT` from the shell; `.env` is not loaded automatically. See [OpenAlex client setup](docs/development.md#openalex-client) for configuration, rate limits, and test commands. The probe prints only work IDs, years, and titles; it does not import works.

## Import works

After `make migrate`, run `go run ./cmd/importer --search 'graph databases' --limit 10` with `DATABASE_URL` exported. The importer writes papers, topics, paper-topic scores and citations between imported papers. It retains unresolved references until their target works are imported. For 100 or more works, export `OPENALEX_API_KEY` and increase `--limit` gradually. Optional `--from-year` and `--to-year` flags narrow the API results before applying the limit. See [importing works](docs/development.md#importing-works) for checks and rollback notes.
