#!/usr/bin/env bash
set -euo pipefail

deadline=$((SECONDS + 90))
until ./scripts/compose.sh exec -T db sh -c 'pg_isready -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
        printf 'PostgreSQL did not become ready within 90 seconds. Run make logs for details.\n' >&2
        exit 1
    fi
    sleep 1
done

printf 'PostgreSQL is ready.\n'
