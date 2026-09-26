# Linux development

This setup uses one `compose.yaml` on Ubuntu and Fedora. It requires Git, Go 1.25 or newer for the API, and a working Compose provider. Python and uv are optional until the ML phase. No desktop services are used, so Fedora Workstation with Hyprland works the same as a terminal session on another Linux desktop.

Ubuntu 25.04 compatibility is a target, but that release is past its support period. Use a supported Ubuntu release for a machine exposed to untrusted networks. Install Docker Engine and the Docker Compose plugin through your chosen package source, or use Podman; this repository does not install system packages. Ensure your user can invoke the selected provider.

On Fedora 44, install Podman and a Compose provider (for example `podman-compose`) through Fedora packages. Rootless Podman is suitable. `scripts/compose.sh` selects `docker compose`, then `podman compose`, then `podman-compose`, in that order. If both Docker and Podman are installed, Docker Compose takes priority.

```bash
cp .env.example .env
# Edit POSTGRES_PASSWORD in .env before starting.
make doctor
make up
make ps
make db-shell
```

The database is available only on `127.0.0.1:${PG_PORT:-5432}` on the host. Set `PG_PORT` in `.env` if 5432 is already occupied. The database name is `scigraph`. A named volume stores PostgreSQL 18 data under `/var/lib/postgresql`; PostgreSQL 18 container images use this parent mount to support their versioned data directory. The volume survives `make down` and container replacement.

Fedora enables SELinux by default. A named volume lets the container engine handle the database storage context, avoiding permissions and labeling problems caused by a host directory bind mount. Do not disable SELinux or make the data directory world writable.

`make up` starts the service and waits for PostgreSQL TCP readiness; Compose also monitors the healthcheck. `make logs` follows the database logs; `make down` stops the service while retaining data. `make db-reset` prompts before deleting the database volume. The PostgreSQL container includes `psql`, so a host installation of `psql` is unnecessary.

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

The extension creation above is a manual environment check. A later migration will enable it for application databases.

## Go API configuration

The Go process reads the shell environment. `.env` configures Compose but is not loaded automatically by Go; export `DATABASE_URL` before `make run`. The DSN must match the credentials in `.env` and use `sslmode=disable` only for the local loopback database. URL-encode special characters in the password. The defaults below apply when a variable is absent:

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | HTTP listener |
| `DATABASE_URL` | required | PostgreSQL connection URL |
| `HTTP_READ_TIMEOUT` | `5s` | Request read timeout |
| `HTTP_WRITE_TIMEOUT` | `10s` | Response write timeout |
| `HTTP_IDLE_TIMEOUT` | `60s` | Idle connection timeout |
| `DB_CONNECT_TIMEOUT` | `3s` | Connection attempt timeout |
| `DB_PING_TIMEOUT` | `3s` | Readiness check timeout |

Run `make run` in one terminal and send `curl` requests from another while it remains running. Ctrl+C ends the process, so later requests will get connection refused; Make may report `Error 1` because the foreground command was interrupted. The API starts even when the database is offline; `/readyz` then returns 503. It closes the listener gracefully on SIGINT or SIGTERM and waits up to 10 seconds for active requests. Run `make fmt`, `go vet ./...`, and `make test` after Go changes.

## Migrations

Export `DATABASE_URL` as for the API and run:

```bash
make migrate-status
make migrate
make migrate-status
```

`make migrate` applies pending SQL files in a transaction. `make migrate-down` rolls back one version and deletes the tables and their data; the first rollback also removes the `vector` extension. PostgreSQL refuses to drop the extension if another object depends on it. The version ledger (`schema_migrations`) remains after rollback. Applied migration checksums are verified before subsequent changes, so edit an applied SQL file only by creating a new migration instead.

For a migration cycle on a **disposable database**:

```bash
export DATABASE_URL_TEST='postgres://scigraph:<your-password>@127.0.0.1:5432/scigraph_test?sslmode=disable'
go test -tags integration ./db/migrations
```

Create `scigraph_test` separately before running this command and use credentials that can create the extension. The integration test applies the schema, checks constraints, rolls it back, applies it again, and cleans up. Never point `DATABASE_URL_TEST` at a database with useful data.
