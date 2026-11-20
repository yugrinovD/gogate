# API

[English](API.en.md)

Машиночитаемые контракты (OpenAPI 3.1, English):

- [api/openapi.public.yaml](../api/openapi.public.yaml)
- [api/openapi.admin.yaml](../api/openapi.admin.yaml)

## Публичный API

Базовый URL: `http://localhost:8080`.

Аутентификация: `Authorization: Bearer gk_...`.

| Метод | Путь | Назначение |
| --- | --- | --- |
| POST | `/v1/chat/completions` | Chat completion, JSON или SSE |
| GET | `/v1/models` | Модели включённых провайдеров |
| GET | `/healthz` | Liveness |
| GET | `/readyz` | Readiness (PostgreSQL + Redis) |
| GET | `/metrics` | Prometheus |

### Chat completion

Минимальный запрос:

```json
{
  "model": "mock",
  "messages": [{"role": "user", "content": "Hello"}]
}
```

Поддерживаемые поля: `model`, `messages`, `temperature`, `top_p`, `n` (только 1),
`max_tokens`, `max_completion_tokens`, `stop`, `seed`, `presence_penalty`,
`frequency_penalty`, `stream`, `stream_options.include_usage`, `user`.

Ограничения: до 128 сообщений, до 4096 выходных токенов (настраивается),
body limit 1 MiB. Content может быть строкой или массивом text/input_text.

### Streaming

```json
{
  "model": "mock",
  "messages": [{"role": "user", "content": "Stream"}],
  "stream": true,
  "stream_options": {"include_usage": true}
}
```

Ответ: `Content-Type: text/event-stream`.

```text
data: {"id":"...","object":"chat.completion.chunk",...}

data: [DONE]
```

При ошибке upstream после первого байта отправляется финальный
`data: {"error":{...}}` и `data: [DONE]`; fallback в этот момент уже запрещён.

### Заголовки ответа

| Заголовок | Значение |
| --- | --- |
| `X-Request-ID` | request id (входящий или сгенерированный) |
| `X-Gogate-Provider` | фактический провайдер |
| `X-Gogate-Cache` | `HIT`, `MISS`, `SEMANTIC`, `BYPASS` |
| `Retry-After` | секунды до повтора при `429` |

### Ошибки

```json
{
  "error": {
    "message": "limit exceeded: rpm",
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded"
  }
}
```

| HTTP | code (пример) | Причина |
| --- | --- | --- |
| 400 | `invalid_request`, `invalid_messages` | неверные поля |
| 401 | `invalid_api_key` | нет/неверный ключ |
| 404 | `model_not_found` | модель не сконфигурирована |
| 413 | `request_too_large` | превышен body limit |
| 429 | `rate_limit_exceeded`, `insufficient_quota` | RPM/TPM/квота/бюджет |
| 502 | `upstream_error` | upstream 5xx/network или ошибка конфигурации auth |
| 503 | `all_providers_unavailable` | все breaker'ы открыты |
| 504 | `upstream_timeout` | таймаут первого байта или idle стрима |

## Admin API

Базовый URL: `http://localhost:9090`.

Аутентификация: `Authorization: Bearer $GOGATE_ADMIN_TOKEN`.

| Метод | Путь | Назначение |
| --- | --- | --- |
| GET | `/admin/v1/health` | DB/Redis/провайдеры/breaker'ы |
| GET | `/admin/v1/config` | конфиг без секретов |
| POST | `/admin/v1/reload` | hot reload control plane |
| GET/POST | `/admin/v1/tenants` | список/создание tenant |
| DELETE | `/admin/v1/tenants/{id}` | удалить tenant |
| GET/POST | `/admin/v1/keys` | список/создание ключа |
| PATCH/DELETE | `/admin/v1/keys/{id}` | enable/disable/delete |
| GET/POST | `/admin/v1/providers` | список/создание провайдера |
| PATCH/DELETE | `/admin/v1/providers/{id}` | частичное изменение; name immutable / удаление |
| POST | `/admin/v1/providers/{id}/test` | health check провайдера |
| GET/POST | `/admin/v1/quotas` | список/upsert квоты |
| DELETE | `/admin/v1/quotas/{id}` | удалить квоту |
| GET/POST | `/admin/v1/prices` | список/upsert цен |
| DELETE | `/admin/v1/prices/{id}` | удалить цену |
| GET | `/admin/v1/usage` | дневная агрегация |
| GET | `/admin/v1/audit` | аудит без prompt/response |
| POST | `/admin/v1/cache/purge` | очистка semantic/exact кэша |

### Создание ключа

```bash
curl -s -X POST http://localhost:9090/admin/v1/keys \
  -H "Authorization: Bearer $GOGATE_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"tenant_id":"'$TENANT_ID'","name":"demo"}'
```

`key` в ответе показывается один раз. В БД хранится только hash.

### Квоты

```json
{
  "tenant_id": "uuid",
  "api_key_id": "uuid или null",
  "rpm": 100,
  "tpm": 100000,
  "daily_requests": 10000,
  "daily_tokens": 1000000,
  "monthly_cost_usd": 10
}
```

Квота ключа и квота тенанта применяются одновременно. `null`/отсутствие поля —
без лимита.

### Провайдер

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

`api_key_env` задаёт имя env-переменной с ключом upstream; сам секрет в БД не
хранится. `config` для mock: `latency_ms`, `chunk_delay_ms`,
`fail_before_first`, `fail_after_chunks`, `fail_rate`.
