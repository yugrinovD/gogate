#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -f .env ]]; then
  echo ".env already exists; leaving it untouched."
  exit 0
fi

rand() { openssl rand -hex "${1:-24}"; }

sed \
  -e "s|replace-with-a-random-admin-token|$(rand 32)|g" \
  -e "s|replace-with-at-least-16-random-characters|$(rand 32)|g" \
  -e "s|replace-with-a-random-redis-password|$(rand 24)|g" \
  -e "s|replace-with-a-random-postgres-password|$(rand 24)|g" \
  -e "s|replace-with-a-random-grafana-password|$(rand 24)|g" \
  .env.example > .env
chmod 600 .env
echo "Created .env with generated secrets."
