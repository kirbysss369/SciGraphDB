#!/usr/bin/env bash
set -euo pipefail

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    provider=(docker compose)
elif command -v podman >/dev/null 2>&1 && podman compose version >/dev/null 2>&1; then
    provider=(podman compose)
elif command -v podman-compose >/dev/null 2>&1 && podman-compose --version >/dev/null 2>&1; then
    provider=(podman-compose)
else
    printf 'No Compose provider found. Install docker compose, podman compose, or podman-compose.\n' >&2
    exit 1
fi

if [[ ${1:-} == --provider ]]; then
    printf '%s\n' "${provider[*]}"
    exit 0
fi

exec "${provider[@]}" "$@"
