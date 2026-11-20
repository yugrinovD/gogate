#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p build
set -a
# shellcheck disable=SC1091
source ./.env
set +a

ADMIN="http://127.0.0.1:${GOGATE_ADMIN_PORT:-9090}"
AUTH="Authorization: Bearer ${GOGATE_ADMIN_TOKEN}"
JSON="Content-Type: application/json"
PUBLIC="http://127.0.0.1:${GOGATE_HTTP_PORT:-8080}"

# Make the recorded rate-limit demo deterministic: create a fresh 2 RPM key.
TENANT_ID=$(curl -fsS "${ADMIN}/admin/v1/tenants" -H "${AUTH}" | jq -r '.[] | select(.name=="demo") | .id' | head -n1)
if [[ -z "${TENANT_ID}" ]]; then
  echo "demo tenant not found; run make demo first" >&2
  exit 1
fi
RL_JSON=$(curl -fsS -X POST "${ADMIN}/admin/v1/keys" -H "${AUTH}" -H "${JSON}" \
  -d "{\"tenant_id\":\"${TENANT_ID}\",\"name\":\"gif-rate-limit-$(date +%s)\"}")
RL_KEY=$(echo "${RL_JSON}" | jq -r .key)
RL_ID=$(echo "${RL_JSON}" | jq -r .id)
curl -fsS -X POST "${ADMIN}/admin/v1/quotas" -H "${AUTH}" -H "${JSON}" \
  -d "{\"tenant_id\":\"${TENANT_ID}\",\"api_key_id\":\"${RL_ID}\",\"rpm\":2,\"tpm\":1000000}" >/dev/null
printf '%s\n' "${RL_KEY}" > build/demo-rate-limit-key.txt
printf '%s\n' "${PUBLIC}" > build/demo-url.txt
chmod 600 build/demo-rate-limit-key.txt

if command -v vhs >/dev/null 2>&1; then
  vhs docs/demo.tape
  echo "wrote docs/demo.gif"
  exit 0
fi

if docker image inspect ghcr.io/charmbracelet/vhs:latest >/dev/null 2>&1 || docker pull ghcr.io/charmbracelet/vhs:latest >/dev/null 2>&1; then
  docker run --rm --network host -v "${PWD}:/vhs" -w /vhs ghcr.io/charmbracelet/vhs:latest docs/demo.tape
  if [[ "$(stat -c %u docs/demo.gif)" != "$(id -u)" ]]; then
    mv docs/demo.gif docs/demo.gif.tmp
    cp docs/demo.gif.tmp docs/demo.gif
    rm docs/demo.gif.tmp
  fi
  echo "wrote docs/demo.gif"
  exit 0
fi

echo "VHS is not available. Install it or run the tape container manually:" >&2
echo "  docker run --rm --network host -v \"$PWD:/vhs\" -w /vhs ghcr.io/charmbracelet/vhs docs/demo.tape" >&2
exit 1
