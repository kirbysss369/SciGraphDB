# Linux development

This setup uses one `compose.yaml` on Ubuntu and Fedora. It requires Git, Go 1.25 or newer for the API, and a working Compose provider. Python and uv are optional until the ML phase. No desktop services are used, so Fedora Workstation with Hyprland works the same as a terminal session on another Linux desktop.

Ubuntu 25.04 compatibility is a target, but that release is past its support period. Use a supported Ubuntu release for a machine exposed to untrusted networks. Install Docker Engine and the Docker Compose plugin through your chosen package source, or use Podman; this repository does not install system packages. Ensure your user can invoke the selected provider.

On Fedora 44, install Podman and a Compose provider (for example `podman-compose`) through Fedora packages. Rootless Podman is suitable. `scripts/compose.sh` selects `docker compose`, then `podman compose`, then `podman-compose`, in that order. If both Docker and Podman are installed, Docker Compose takes priority.

```bash
cp .env.example .env
# Edit POSTGRES_PASSWORD in .env before starting.
make doctor
make dev
```

`make dev` starts the container, checks the credentials, applies the migrations, and runs the API in the foreground. In a second terminal, use `make ps`, `make db-shell`, and `curl http://127.0.0.1:8080/readyz`. Ctrl+C stops the API; `make down` also stops the database. Use `make up` and `make run` if you want to start them separately.

The database is available only on `127.0.0.1:${PG_PORT:-5432}` on the host. Set `PG_PORT` in `.env` if 5432 is already occupied. The database name is `scigraph`. A named volume stores PostgreSQL 18 data under `/var/lib/postgresql`; PostgreSQL 18 container images use this parent mount to support their versioned data directory. The volume survives `make down` and container replacement.

Fedora enables SELinux by default. A named volume lets the container engine handle the database storage context, avoiding permissions and labeling problems caused by a host directory bind mount. Do not disable SELinux or make the data directory world writable.

`make up` starts the service, waits for PostgreSQL, checks password authentication and verifies the Go connection. Compose also monitors the healthcheck. `make logs` follows the database logs; `make down` stops the service while retaining data. `make db-reset` prompts before deleting the database volume. The PostgreSQL container includes `psql`, so a host installation of `psql` is unnecessary.

To verify the image and persistence manually:

```sql
CREATE EXTENSION IF NOT EXISTS vector;
SELECT current_setting('server_version');
SELECT extversion FROM pg_extension WHERE extname = 'vector';
CREATE TABLE IF NOT EXISTS bootstrap_check (id integer PRIMARY KEY);
INSERT INTO bootstrap_check (id) VALUES (1) ON CONFLICT DO NOTHING;
```

Run `make down`, `make up`, and `make db-shell` again, then:

```sql
SELECT extversion FROM pg_extension WHERE extname = 'vector';
SELECT * FROM bootstrap_check;
DROP TABLE bootstrap_check;
```

The extension creation above is a manual environment check; migration 001 also enables it.

## Go API configuration

Go commands read `.env` automatically; set `POSTGRES_PASSWORD` there once. They derive a loopback connection URL from `POSTGRES_USER`, `POSTGRES_PASSWORD`, and `PG_PORT` and escape password punctuation automatically. Shell variables override values in `.env`. Use single quotes for a value containing `$` or spaces, such as `POSTGRES_PASSWORD='a$b c'`; interpolation syntax is rejected to avoid disagreement with Compose. Keep `.env` private. An explicitly exported `DATABASE_URL` is supported for direct `go run` commands and external databases; the local Make targets clear that override so a stale exported URL cannot break the local workflow. The defaults below apply when a variable is absent:

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | HTTP listener |
| `POSTGRES_PASSWORD` | required | Local container and Go database password |
| `POSTGRES_USER` | `scigraph` | Local database user |
| `PG_PORT` | `5432` | Local host port |
| `DATABASE_URL` | derived for local use | Optional explicit URL for direct Go commands and CI |
| `HTTP_READ_TIMEOUT` | `5s` | Request read timeout |
| `HTTP_WRITE_TIMEOUT` | `10s` | Response write timeout |
| `HTTP_IDLE_TIMEOUT` | `60s` | Idle connection timeout |
| `DB_CONNECT_TIMEOUT` | `3s` | Connection attempt timeout |
| `DB_PING_TIMEOUT` | `3s` | Readiness check timeout |

Run `make run` in one terminal and send `curl` requests from another while it remains running. Ctrl+C ends the process, so later requests will get connection refused; Make may report `Error 1` because the foreground command was interrupted. The API starts even when the database is offline; `/readyz` then returns 503. It closes the listener gracefully on SIGINT or SIGTERM and waits up to 10 seconds for active requests. Run `make fmt`, `go vet ./...`, and `make test` after Go changes.

If `/healthz` returns 200 but `/readyz` returns 503, run `make ps`, `make migrate-status` and `make logs`. If you edit `POSTGRES_PASSWORD` in `.env` after the database volume exists, `make up` updates the local database role through its container socket and verifies a TCP login. This preserves your data. Other programs using the old password must update their credentials. Container initialization itself only uses `POSTGRES_PASSWORD` on an empty volume.

If `make up` cannot change the role password, inspect the error and run `make db-shell`, then `\password scigraph` at the `psql` prompt. Do not use `make db-reset` for a password mismatch: it deletes the database volume.

## Migrations

Run against the local database after `make up`:

```bash
make migrate-status
make migrate
make migrate-status
```

`make migrate` applies pending SQL files in a transaction. `make migrate-down` rolls back one version: version 002 drops the unresolved citation queue, while version 001 drops the core tables and their data and removes the `vector` extension. PostgreSQL refuses to drop the extension if another object depends on it. The version ledger (`schema_migrations`) remains after rollback. Applied migration checksums are verified before subsequent changes, so edit an applied SQL file only by creating a new migration instead.

For a migration cycle on a **disposable database**:

```bash
export DATABASE_URL_TEST='postgres://scigraph:<your-password>@127.0.0.1:5432/scigraph_test?sslmode=disable'
go test -tags integration ./db/migrations
```

Create `scigraph_test` separately before running this command and use credentials that can create the extension. The integration test applies the schema, checks constraints, rolls it back, applies it again, and cleans up. Never point `DATABASE_URL_TEST` at a database with useful data.

## OpenAlex client

`cmd/openalex-probe` queries works without using PostgreSQL. Set these variables in `.env` or the shell:

| Variable | Default | Purpose |
| --- | --- | --- |
| `OPENALEX_BASE_URL` | `https://api.openalex.org` | API origin, or a local test server |
| `OPENALEX_API_KEY` | unset | Optional Bearer token; required by the probe for more than 10 works |
| `OPENALEX_TIMEOUT` | `10s` | Per-attempt request and response timeout |

For a small manual network check, run:

```bash
go run ./cmd/openalex-probe --search 'graph databases' --limit 3
```

For a larger probe, set `OPENALEX_API_KEY` in your ignored `.env` or a private shell. Keep it out of command arguments, logs, and commits. Requests use `select` for the fields the client reads, at most 100 results per page, and `meta.next_cursor` for pagination. Transient HTTP 429 and 5xx responses get at most three attempts with short backoff. A zero remaining daily budget or a `Retry-After` beyond the five-second retry window fails promptly so a probe cannot wait all day. The maximum client search limit is 10,000 works; use an OpenAlex snapshot for bulk exports. API responses and abstracts are kept in memory only.

Tests use local `httptest.Server` fixtures and never need a public API call:

```bash
go fmt ./...
go vet ./...
go test ./...
```

The public probe above is optional. OpenAlex documents [authentication and rate limits](https://help.openalex.org/api/authentication/), [cursor paging](https://help.openalex.org/api/paging/), [work attributes](https://help.openalex.org/data/works/attributes/), and [error handling](https://help.openalex.org/api/errors/). Check these before substantially changing the client because API policies can change.

## Importing works

Apply both migrations (`make migrate`), then run a small import:

```bash
go run ./cmd/importer --search 'graph databases' --from-year 2020 --to-year 2024 --limit 10
```

The year bounds are inclusive OpenAlex publication-date filters and are applied before `--limit`. Without a key, the CLI permits at most 10 works. For a 100-work sample, set `OPENALEX_API_KEY` privately in `.env` or the shell and repeat with `--limit 100`; inspect the rows and daily API budget before trying `--limit 1000`. The client fetches at most 10,000 works per invocation. No automated test calls the public API, and a keyless probe/import should stay small.

Each batch of up to 100 works commits atomically. `papers` uses `ON CONFLICT (openalex_id) DO UPDATE` to replace DOI, title, abstract, year, citation count, metadata and `updated_at` while preserving `id` and `created_at`. `topics` updates names on `openalex_id` conflict. The import replaces each work's outgoing `paper_topics` and citations, then inserts topic pairs with `ON CONFLICT (paper_id, topic_id) DO UPDATE` for scores and queues unresolved references with `ON CONFLICT (citing_paper_id, cited_openalex_id) DO NOTHING`. Once both papers exist, a citation is inserted in the citing → cited direction with `ON CONFLICT (citing_paper_id, cited_paper_id) DO NOTHING`, and its queue entry is removed. Re-importing does not duplicate rows; removing a reference from OpenAlex removes that outgoing edge on the next import of its citing paper. Incoming edges from other works are retained. `metadata` stores only source and reference count, not raw API responses.

The summary reports `fetched` API works, `inserted` new papers, `updated` existing papers processed (including unchanged values), `skipped` malformed/duplicate works, `failed` works in a failed database batch, and `citations_linked` edges inserted during this invocation. A failed batch rolls back; earlier committed batches remain. Progress is logged once per committed batch. SIGINT/SIGTERM cancels requests and database work. Do not roll back migration 002 to retry an import: that drops unresolved references.

Check a 10-work sample, then a 100-work sample in `make db-shell`:

```sql
SELECT count(*) FROM papers;
SELECT count(*) FROM topics;
SELECT count(*) FROM paper_topics;
SELECT count(*) FROM citations;
SELECT count(*) FROM pending_citations;
SELECT openalex_id, count(*) FROM papers GROUP BY openalex_id HAVING count(*) > 1;
SELECT id, title, publication_year FROM papers ORDER BY cited_by_count DESC LIMIT 10;
```

Repeat the same import and compare table counts. Citation count may be low in a small sample because only references to already imported papers become edges. To run the deterministic fixture integration test, use an **empty disposable database** with `DATABASE_URL_TEST`:

```bash
go test -tags integration ./internal/importer -v
```
