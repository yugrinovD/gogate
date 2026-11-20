# API

[Russian](API.md)

Machine-readable contracts (OpenAPI 3.1, English):

- [api/openapi.public.yaml](../api/openapi.public.yaml)
- [api/openapi.admin.yaml](../api/openapi.admin.yaml)

## Public API

Base URL: `http://localhost:8080`.

Authentication: `Authorization: Bearer gk_...`.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/v1/chat/completions` | Chat completion, JSON or SSE |
| GET | `/v1/models` | Models exposed by enabled providers |
| GET | `/healthz` | Liveness |
| GET | `/readyz` | Readiness (PostgreSQL + Redis) |
| GET | `/metrics` | Prometheus |

### Chat completion

Minimal request:

```json
{
  "model": "mock",
  "messages": [{"role": "user", "content": "Hello"}]
}
```

Supported fields: `model`, `messages`, `temperature`, `top_p`, `n` (1 only),
`max_tokens`, `max_completion_tokens`, `stop`, `seed`, `presence_penalty`,
`frequency_penalty`, `stream`, `stream_options.include_usage`, `user`.

Limits: up to 128 messages, up to 4096 output tokens (configurable), 1 MiB body
limit. Content can be a string or an array of text/input_text parts.

### Streaming

```json
{
  "model": "mock",
  "messages": [{"role": "user", "content": "Stream"}],
  "stream": true,
  "stream_options": {"include_usage": true}
}
```

Response: `Content-Type: text/event-stream`.

```text
data: {"id":"...","object":"chat.completion.chunk",...}

data: [DONE]
```

If the upstream fails after the first byte, a final `data: {"error":{...}}`
frame and `data: [DONE]` are sent; fallback is forbidden at that point.

### Response headers

| Header | Value |
| --- | --- |
| `X-Request-ID` | incoming or generated request id |
| `X-Gogate-Provider` | actual provider |
| `X-Gogate-Cache` | `HIT`, `MISS`, `SEMANTIC`, `BYPASS` |
| `Retry-After` | seconds until retry on `429` |

### Errors

```json
{
  "error": {
    "message": "limit exceeded: rpm",
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded"
  }
}
```

| HTTP | code (example) | Reason |
| --- | --- | --- |
| 400 | `invalid_request`, `invalid_messages` | invalid fields |
| 401 | `invalid_api_key` | missing/invalid key |
| 404 | `model_not_found` | model is not configured |
| 413 | `request_too_large` | body limit exceeded |
| 429 | `rate_limit_exceeded`, `insufficient_quota` | RPM/TPM/quota/budget |
| 502 | `upstream_error` | upstream 5xx/network or auth configuration error |
| 503 | `all_providers_unavailable` | all breakers open |
| 504 | `upstream_timeout` | first-byte or stream-idle timeout |

## Admin API

Base URL: `http://localhost:9090`.

Authentication: `Authorization: Bearer $GOGATE_ADMIN_TOKEN`.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/admin/v1/health` | DB/Redis/providers/breakers |
| GET | `/admin/v1/config` | redacted config |
| POST | `/admin/v1/reload` | control-plane hot reload |
| GET/POST | `/admin/v1/tenants` | list/create tenant |
| DELETE | `/admin/v1/tenants/{id}` | delete tenant |
| GET/POST | `/admin/v1/keys` | list/create key |
| PATCH/DELETE | `/admin/v1/keys/{id}` | enable/disable/delete |
| GET/POST | `/admin/v1/providers` | list/create provider |
| PATCH/DELETE | `/admin/v1/providers/{id}` | partial update; name is immutable / delete |
| POST | `/admin/v1/providers/{id}/test` | provider health check |
| GET/POST | `/admin/v1/quotas` | list/upsert quota |
| DELETE | `/admin/v1/quotas/{id}` | delete quota |
| GET/POST | `/admin/v1/prices` | list/upsert prices |
| DELETE | `/admin/v1/prices/{id}` | delete price |
| GET | `/admin/v1/usage` | daily aggregates |
| GET | `/admin/v1/audit` | audit without prompt/response |
| POST | `/admin/v1/cache/purge` | purge semantic/exact cache |

### Create a key

```bash
curl -s -X POST http://localhost:9090/admin/v1/keys \
  -H "Authorization: Bearer $GOGATE_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"tenant_id":"'$TENANT_ID'","name":"demo"}'
```

`key` is returned once. Only its hash is stored.

### Quotas

```json
{
  "tenant_id": "uuid",
  "api_key_id": "uuid or null",
  "rpm": 100,
  "tpm": 100000,
  "daily_requests": 10000,
  "daily_tokens": 1000000,
  "monthly_cost_usd": 10
}
```

Key and tenant quotas are enforced together. `null`/missing means unlimited.

### Provider

```json
{
  "name": "ollama",
  "kind": "ollama",
  "base_url": "http://ollama:11434",
  "models": ["qwen2.5:3b"],
  "priority": 10,
  "enabled": true,
  "timeout_ms": 30000,
  "max_retries": 1,
  "config": {}
}
```

`api_key_env` names the env variable holding the upstream key; it must start
with `GOGATE_UPSTREAM_`, and the secret is not stored in the database. Mock `config` keys: `latency_ms`, `chunk_delay_ms`,
`fail_before_first`, `fail_after_chunks`, `fail_rate`.
