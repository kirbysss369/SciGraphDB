# SciGraphDB

SciGraphDB is a research project for scientific paper retrieval and relation analysis. The initial repository contains only the development environment: PostgreSQL 18 with pgvector 0.8.6, a Compose wrapper, and directories for future Go, Python, and experiments.

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

No application code or schema migrations have been added yet.
