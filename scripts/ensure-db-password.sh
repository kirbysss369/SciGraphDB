#!/usr/bin/env bash
set -euo pipefail

# Localhost inside the container can use a different pg_hba rule from clients
# connecting through the published port. Check from the host, like the API.
check_host_connection() {
    env -u DATABASE_URL go run ./cmd/migrate status >/dev/null 2>&1
}

if ! check_host_connection; then
    printf 'Updating the local PostgreSQL role password from .env...\n'
    if ! ./scripts/compose.sh exec -T db sh -c '
        exec psql -X -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"
    ' >/dev/null <<'PSQL'
\getenv role POSTGRES_USER
\getenv new_password POSTGRES_PASSWORD
SELECT format('ALTER ROLE %I PASSWORD %L', :'role', :'new_password') \gexec
PSQL
    then
        printf 'Could not update the database password. Run make db-shell and use \\password scigraph.\n' >&2
        exit 1
    fi
    if ! check_host_connection; then
        printf 'Host database connection still failed. Check PG_PORT, .env and make logs.\n' >&2
        exit 1
    fi
fi
printf 'PostgreSQL credentials verified.\n'
