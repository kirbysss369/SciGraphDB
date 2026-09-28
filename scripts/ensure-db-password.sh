#!/usr/bin/env bash
set -euo pipefail

# Compose has already loaded .env into the container. Check the actual TCP
# password: pg_isready only checks whether PostgreSQL accepts connections.
check_password() {
    ./scripts/compose.sh exec -T db sh -c '
        PGPASSWORD="$POSTGRES_PASSWORD" exec psql -X -qAt \
            -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
            -c "SELECT 1"
    ' >/dev/null 2>&1
}

if ! check_password; then
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
    if ! check_password; then
        printf 'Database password check still failed. Check .env and make logs.\n' >&2
        exit 1
    fi
fi

# Verify that the Go process derives the same credentials and host port.
if ! env -u DATABASE_URL go run ./cmd/migrate status >/dev/null; then
    printf 'Host connection failed. Check PG_PORT and .env; make down then make up retains data if the container environment is stale.\n' >&2
    exit 1
fi
printf 'PostgreSQL credentials verified.\n'
