# SciGraphDB

SciGraphDB is a research project for scientific paper retrieval and relation analysis. The repository currently contains a minimal Go API, PostgreSQL 18 with pgvector 0.8.6, and directories for future research work.

## Start locally

```bash
cp .env.example .env
# Set POSTGRES_PASSWORD in .env to a private local password.
make doctor
make dev
```

`make dev` starts PostgreSQL, verifies its password, applies migrations and runs the API in the foreground. In another terminal, run `curl http://127.0.0.1:8080/healthz` and `curl http://127.0.0.1:8080/readyz`. Press Ctrl+C to stop the API; `make down` stops PostgreSQL but preserves its data. `make db-reset` deletes the database volume and requires confirmation. Ubuntu and Fedora details are in [docs/development.md](docs/development.md).

## API

To run the API separately after `make up`, use:

```bash
make run
```

While the API shows `HTTP server listening`, use another terminal:

```bash
curl --max-time 5 http://127.0.0.1:8080/healthz
curl --max-time 5 http://127.0.0.1:8080/readyz
```

`make run` stays in the foreground. Pressing Ctrl+C stops the API, after which `curl` cannot connect; Make may print `Error 1` for the interrupted command. `GET /healthz` returns 200 while the process is running. `GET /readyz` returns 200 when PostgreSQL responds to a ping and 503 otherwise. `make fmt` and `make test` format and test the Go packages.

## Database schema

With PostgreSQL running, use `make migrate` to create the tables and pgvector extension. `make migrate-status` shows applied versions. `make migrate-down` rolls back the latest migration and **deletes its data**. The SQL files are under `db/migrations/`; the runner is part of the Go project. See [the migration procedure](docs/development.md#migrations). Migration 003 adds a 384-dimensional paper vector without an ANN index.

## OpenAlex probe

The read-only works client can query OpenAlex without a database. For a small keyless check, run `go run ./cmd/openalex-probe --search 'graph databases' --limit 3`. Set `OPENALEX_API_KEY` in `.env` for larger probes. See [OpenAlex client setup](docs/development.md#openalex-client) for configuration, rate limits, and test commands. The probe prints only work IDs, years, and titles; it does not import works.

## Import works

After `make migrate`, run `go run ./cmd/importer --search 'graph databases' --limit 10`. The importer writes papers, topics, paper-topic scores and citations between imported papers. It retains unresolved references until their target works are imported. For 100 or more works, set `OPENALEX_API_KEY` in `.env` and increase `--limit` gradually. Optional `--from-year` and `--to-year` flags narrow the API results before applying the limit. See [importing works](docs/development.md#importing-works) for checks and rollback notes.

## Paper embeddings

Install [uv](https://docs.astral.sh/uv/) separately, then run `make embeddings` after importing papers. It starts PostgreSQL, applies the current migrations, installs locked Python dependencies, and scans the first 100 papers with the pinned CPU model. `make embeddings ML_LIMIT=0` scans all papers in bounded batches. Repeating the command skips unchanged vectors. See [the embedding workflow](docs/development.md#paper-embeddings) for the model, data checks, and rollback.
