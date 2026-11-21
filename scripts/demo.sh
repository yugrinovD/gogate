#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

OLLAMA=0
if [[ "${1:-}" == "--ollama" ]]; then
  OLLAMA=1
fi

mkdir -p build
set -a
# shellcheck disable=SC1091
source ./.env
set +a

ADMIN="http://127.0.0.1:${GOGATE_ADMIN_PORT:-9090}"
PUBLIC="http://127.0.0.1:${GOGATE_HTTP_PORT:-8080}"
AUTH="Authorization: Bearer ${GOGATE_ADMIN_TOKEN}"
JSON="Content-Type: application/json"

echo "==> starting gogate, PostgreSQL, Redis, Prometheus, Grafana and Jaeger"
docker compose up -d --build postgres redis gogate prometheus grafana jaeger

echo "==> waiting for readiness"
for _ in $(seq 1 90); do
  if curl -fsS "${PUBLIC}/readyz" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
if ! curl -fsS "${PUBLIC}/readyz" >/dev/null; then
  echo "gateway did not become ready" >&2
  docker compose logs --tail=80 gogate >&2 || true
  exit 1
fi

echo "==> creating demo tenant, keys and quotas"
NONCE="$(date +%s)"
TENANT_ID=$(curl -fsS -X POST "${ADMIN}/admin/v1/tenants" -H "${AUTH}" -H "${JSON}" -d '{"name":"demo"}' | jq -r .id)
KEY_JSON=$(curl -fsS -X POST "${ADMIN}/admin/v1/keys" -H "${AUTH}" -H "${JSON}" -d "{\"tenant_id\":\"${TENANT_ID}\",\"name\":\"demo\"}")
API_KEY=$(echo "${KEY_JSON}" | jq -r .key)

curl -fsS -X POST "${ADMIN}/admin/v1/quotas" -H "${AUTH}" -H "${JSON}" \
  -d "{\"tenant_id\":\"${TENANT_ID}\",\"rpm\":100000,\"tpm\":100000000,\"daily_requests\":10000000,\"daily_tokens\":10000000000,\"monthly_cost_usd\":1000}" >/dev/null

RL_KEY_JSON=$(curl -fsS -X POST "${ADMIN}/admin/v1/keys" -H "${AUTH}" -H "${JSON}" -d "{\"tenant_id\":\"${TENANT_ID}\",\"name\":\"demo-rate-limit\"}")
RL_KEY=$(echo "${RL_KEY_JSON}" | jq -r .key)
RL_KEY_ID=$(echo "${RL_KEY_JSON}" | jq -r .id)
curl -fsS -X POST "${ADMIN}/admin/v1/quotas" -H "${AUTH}" -H "${JSON}" \
  -d "{\"tenant_id\":\"${TENANT_ID}\",\"api_key_id\":\"${RL_KEY_ID}\",\"rpm\":2,\"tpm\":1000000}" >/dev/null

# A fresh key for the recorded GIF, so its rate-limit demo starts from zero.
GIF_RL_JSON=$(curl -fsS -X POST "${ADMIN}/admin/v1/keys" -H "${AUTH}" -H "${JSON}" -d "{\"tenant_id\":\"${TENANT_ID}\",\"name\":\"demo-gif-rate-limit\"}")
GIF_RL_KEY=$(echo "${GIF_RL_JSON}" | jq -r .key)
GIF_RL_ID=$(echo "${GIF_RL_JSON}" | jq -r .id)
curl -fsS -X POST "${ADMIN}/admin/v1/quotas" -H "${AUTH}" -H "${JSON}" \
  -d "{\"tenant_id\":\"${TENANT_ID}\",\"api_key_id\":\"${GIF_RL_ID}\",\"rpm\":2,\"tpm\":1000000}" >/dev/null

printf '%s\n' "${API_KEY}" > build/demo-key.txt
printf '%s\n' "${GIF_RL_KEY}" > build/demo-rate-limit-key.txt
printf '%s\n' "${PUBLIC}" > build/demo-url.txt
chmod 600 build/demo-key.txt build/demo-rate-limit-key.txt

echo
echo "==> GET /v1/models"
curl -fsS "${PUBLIC}/v1/models" -H "Authorization: Bearer ${API_KEY}" | jq '{object, models: [.data[].id]}'

echo
echo "==> non-streaming chat"
curl -fsS -D build/headers.txt -o build/response.json -X POST "${PUBLIC}/v1/chat/completions" \
  -H "Authorization: Bearer ${API_KEY}" -H "${JSON}" \
  -d "{\"model\":\"mock\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello from gogate ${NONCE}\"}],\"max_tokens\":64,\"temperature\":0.2}"
grep -iE '^(x-gogate-provider|x-gogate-cache|x-request-id):' build/headers.txt || true
jq '{id, model, choices, usage}' build/response.json

echo
echo "==> same request again (exact cache)"
curl -fsS -D build/headers-cache.txt -o /dev/null -X POST "${PUBLIC}/v1/chat/completions" \
  -H "Authorization: Bearer ${API_KEY}" -H "${JSON}" \
  -d "{\"model\":\"mock\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello from gogate ${NONCE}\"}],\"max_tokens\":64,\"temperature\":0.2}"
grep -iE '^(x-gogate-cache|x-gogate-provider):' build/headers-cache.txt || true

echo
echo "==> streaming chat (SSE)"
curl -fsS -N -X POST "${PUBLIC}/v1/chat/completions" \
  -H "Authorization: Bearer ${API_KEY}" -H "${JSON}" \
  -d '{"model":"mock","messages":[{"role":"user","content":"Stream this response"}],"stream":true,"stream_options":{"include_usage":true},"max_tokens":32}' \
  -o build/stream.txt
head -n 12 build/stream.txt

echo
echo "==> rate limit demo (key quota: 2 RPM)"
for i in 1 2 3; do
  code=$(curl -sS -o /dev/null -D "build/rl-${i}.txt" -w '%{http_code}' -X POST "${PUBLIC}/v1/chat/completions" \
    -H "Authorization: Bearer ${RL_KEY}" -H "${JSON}" \
    -d '{"model":"mock","messages":[{"role":"user","content":"rate limit"}],"max_tokens":8}')
  retry=$(grep -i '^retry-after:' "build/rl-${i}.txt" | tr -d '\r' || true)
  echo "request ${i}: HTTP ${code} ${retry}"
done

if [[ "${OLLAMA}" == "1" ]]; then
  echo
  echo "==> starting Ollama and pulling ${OLLAMA_MODEL:-qwen2.5:3b}"
  docker compose --profile ai up -d ollama
  for _ in $(seq 1 60); do
    if docker compose exec -T ollama ollama list >/dev/null 2>&1; then
      break
    fi
    sleep 2
  done
  docker compose exec -T ollama ollama pull "${OLLAMA_MODEL:-qwen2.5:3b}"
  curl -fsS -X POST "${ADMIN}/admin/v1/reload" -H "${AUTH}" >/dev/null
  echo
  echo "==> Ollama chat"
  curl -fsS -X POST "${PUBLIC}/v1/chat/completions" \
    -H "Authorization: Bearer ${API_KEY}" -H "${JSON}" \
    -d "{\"model\":\"${OLLAMA_MODEL:-qwen2.5:3b}\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hello in one short sentence.\"}],\"max_tokens\":64}" \
    | jq '{model, choices, usage}'
fi

echo
echo "==> ready"
echo "    API:        ${PUBLIC}/v1"
echo "    Admin:      ${ADMIN}/admin/v1"
echo "    Metrics:    ${PUBLIC}/metrics"
echo "    Prometheus: http://127.0.0.1:${GOGATE_PROM_PORT:-9092}"
echo "    Grafana:    http://127.0.0.1:${GOGATE_GRAFANA_PORT:-3000} (user: ${GF_SECURITY_ADMIN_USER:-admin})"
echo "    Jaeger:     http://127.0.0.1:${GOGATE_JAEGER_PORT:-16686}"
echo "    Demo key:   build/demo-key.txt"
