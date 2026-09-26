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

With PostgreSQL running, export `DATABASE_URL` and start the API:

```bash
export DATABASE_URL='postgres://scigraph:<your-password>@127.0.0.1:5432/scigraph?sslmode=disable'
make run
curl --max-time 5 http://127.0.0.1:8080/healthz
curl --max-time 5 http://127.0.0.1:8080/readyz
```

`GET /healthz` returns 200 while the process is running. `GET /readyz` returns 200 when PostgreSQL responds to a ping and 503 otherwise. `make fmt` and `make test` format and test the Go packages. No papers, migrations, or OpenAlex code has been added yet.
