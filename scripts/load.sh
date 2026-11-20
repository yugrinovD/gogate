#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p build
set -a
# shellcheck disable=SC1091
source ./.env
set +a

ADMIN="http://127.0.0.1:${GOGATE_ADMIN_PORT:-9090}"
PUBLIC="http://127.0.0.1:${GOGATE_HTTP_PORT:-8080}"
AUTH="Authorization: Bearer ${GOGATE_ADMIN_TOKEN}"
JSON="Content-Type: application/json"

echo "==> starting stack"
docker compose up -d --build postgres redis gogate

for _ in $(seq 1 90); do
  if curl -fsS "${PUBLIC}/readyz" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

TENANT_ID=$(curl -fsS -X POST "${ADMIN}/admin/v1/tenants" -H "${AUTH}" -H "${JSON}" -d '{"name":"loadtest"}' | jq -r .id)
KEY_JSON=$(curl -fsS -X POST "${ADMIN}/admin/v1/keys" -H "${AUTH}" -H "${JSON}" -d "{\"tenant_id\":\"${TENANT_ID}\",\"name\":\"loadtest\"}")
API_KEY=$(echo "${KEY_JSON}" | jq -r .key)
curl -fsS -X POST "${ADMIN}/admin/v1/quotas" -H "${AUTH}" -H "${JSON}" \
  -d "{\"tenant_id\":\"${TENANT_ID}\",\"rpm\":1000000,\"tpm\":1000000000,\"daily_requests\":100000000,\"daily_tokens\":100000000000,\"monthly_cost_usd\":10000}" >/dev/null

echo "==> running k6 (rate=${K6_RATE:-500}/s, duration=${K6_DURATION:-2m})"
K6_API_KEY="${API_KEY}" docker compose --profile load run --rm \
  --user "$(id -u):$(id -g)" \
  -e K6_API_KEY="${API_KEY}" \
  k6 run --summary-export=/reports/k6-summary.json /scripts/chat.js

echo
echo "==> summary written to build/k6-summary.json"
jq '{http_reqs: .metrics.http_reqs, http_req_failed: .metrics.http_req_failed, http_req_duration: .metrics.http_req_duration}' build/k6-summary.json 2>/dev/null || true
