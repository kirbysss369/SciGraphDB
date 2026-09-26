#!/usr/bin/env bash
set -euo pipefail

missing=0

check() {
    local label=$1 kind=$2 binary=$3
    if command -v "$binary" >/dev/null 2>&1; then
        printf '[OK]       %-18s %s\n' "$label" "$(command -v "$binary")"
    else
        printf '[MISSING]  %-18s %s\n' "$label" "$kind"
        if [[ $kind == required ]]; then
            missing=1
        fi
    fi
}

check git required git
check Go required go
check Python optional python3
check uv optional uv
check Docker optional docker
check Podman optional podman
check podman-compose optional podman-compose

if provider=$(./scripts/compose.sh --provider 2>/dev/null); then
    printf '[OK]       %-18s %s\n' 'Compose provider' "$provider"
else
    printf '[MISSING]  %-18s %s\n' 'Compose provider' required
    missing=1
fi

if [[ -f .env ]]; then
    printf '[OK]       %-18s %s\n' '.env' present
else
    printf '[MISSING]  %-18s %s\n' '.env' 'required for make up (copy .env.example and set a password)'
    missing=1
fi

if (( missing )); then
    printf 'Required development prerequisites are missing.\n' >&2
    exit 1
fi

printf 'Development prerequisites found.\n'
