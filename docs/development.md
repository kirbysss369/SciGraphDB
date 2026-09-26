# Linux development

This setup uses one `compose.yaml` on Ubuntu and Fedora. It requires Git, Go for the later backend work, and a working Compose provider. Python and uv are optional until the ML phase. No desktop services are used, so Fedora Workstation with Hyprland works the same as a terminal session on another Linux desktop.

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
