# gogate architecture

[Russian](DESIGN.md)

## Goals

- One OpenAI-compatible API in front of multiple providers.
- Local-first: mock by default, Ollama on demand, no external keys required.
- Predictable behaviour under load: bounded memory, asynchronous writes, no
  PostgreSQL lookups on the hot path.
- Observability and security by default: metrics, traces, JSON logs without
  conversation content, a separate admin surface.
- A clear reliability boundary: retries/fallback/breaker only before the first
  byte of a stream.

## Layers

```text
HTTP (public/admin)
  → middleware: request-id, recover, body limit, CORS, access log
  → auth: API key or admin token
  → rate limit / quotas (Redis Lua)
  → cache (Redis exact, pgvector semantic)
  → orchestrator (retry, fallback, breaker, timeouts)
  → provider adapter (mock / ollama / openai-compatible)
  → async usage/audit writer → PostgreSQL
```

### Public listener

`POST /v1/chat/completions`, `GET /v1/models`, `/healthz`, `/readyz`, `/metrics`.

Request order:

1. Content-Length and body limit checks.
2. `Authorization: Bearer gk_...`: parse prefix, look up the in-memory snapshot,
   check enabled/expiry and `SHA-256(pepper || secret)` in constant time.
3. Validate OpenAI fields: model, messages, temperature, top_p, max_tokens, stop, n=1.
4. Optional request PII redaction.
5. Verify that an enabled provider exposes the requested model.
6. Atomically check RPM/TPM/daily quotas/budget in Redis; reserve tokens and cost;
   on rejection return `429` + `Retry-After`.
7. Exact cache (non-streaming only). A hit refunds the reservation.
8. Optional semantic cache lookup by embedding.
9. Orchestrator opens the stream and commits the first chunk.
10. Streaming: SSE without buffering; non-streaming: aggregate chunks.
11. Settle actual tokens/cost, enqueue usage/audit, emit metrics and a log line.

### Admin listener

Separate port and token. It provides CRUD for tenants, keys, providers, quotas and
prices, health, usage/audit, cache purge, hot reload and optional pprof. Mutations
rebuild the in-memory snapshot.

## Control plane and hot path

- **PostgreSQL** — source of truth: hashed keys, providers, quotas, prices,
  usage, audit, semantic cache.
- **In-memory snapshot** (`atomic.Pointer`) — keys, providers, model → provider
  chains, quotas, prices. Updated by admin mutations and manual reload.
- **Redis** — limits, quotas, exact cache. Not a source of truth.
- **Usage/audit** — batched writer with bounded queues; overflow is dropped and
  recorded in metrics.

This keeps PostgreSQL out of the hot path and lets the gateway restart without
losing configuration.

## Providers

```go
type Provider interface {
    Name() string
    Kind() string
    Models() []string
    Chat(ctx context.Context, req Request) (Stream, error)
    Ping(ctx context.Context) error
}
```

- `mock` — deterministic responses, chunks and usage; can inject errors/delays for tests.
- `ollama` — native `/api/chat`, NDJSON → normalized chunks.
- `openai_compatible` — `/chat/completions`, SSE, Bearer from an env variable.

The registry builds fallback chains: for each model, enabled providers are sorted
by `priority`.

## First-byte boundary

`Orchestrator.Open`:

1. Gets the provider chain.
2. For each provider: retries with backoff+jitter up to `max_retries`.
3. Calls `Chat`, then exactly one `Next` under the first-byte timeout.
4. If the first chunk arrives, the stream is committed and fallback is forbidden.
5. Pre-first-byte errors count as breaker failures; later errors only increment
   the upstream error metric.

This prevents duplicating text that was already sent to the client and makes the
behaviour predictable.

## Rate limiting and quotas

One atomic Redis Lua call per request:

- RPM and TPM as token buckets/GCRA on tenant and key scopes;
- daily request/token counters with TTL;
- monthly cost as `INCRBYFLOAT` with TTL;
- reserve `prompt + max_tokens` and estimated cost, then settle by actual usage.

If Redis is unavailable, the default is fail-closed; `GOGATE_RATE_LIMIT_FAIL_OPEN`
changes that.

## Cache

- **Exact**: Redis, non-streaming only. Key = tenant + model + messages +
  generation parameters. Configurable TTL.
- **Semantic**: pgvector, opt-in, non-streaming only. Prompt embedding, cosine
  similarity, threshold, tenant+model isolation.
- Streaming is never cached.
- Cache hits do not consume upstream tokens: the limit reservation is refunded.

## Usage, cost, audit

- `usage_daily` — upsert by tenant/key/provider/model/day.
- Cost uses `model_prices` (USD per 1M tokens).
- `request_audit` stores metadata only: request_id, tenant, prefix, model,
  provider, status, cache, tokens, cost, latency, error, PII flag.
- A retention job removes old audit rows and expired semantic cache entries.

## Failures and degradation

| Failure | Behaviour |
| --- | --- |
| Provider 5xx/timeout before first byte | retry, then fallback |
| Provider fails after first byte | SSE error, no fallback |
| All breakers open | `503 all_providers_unavailable` |
| Redis unavailable | fail-closed `503` or fail-open by flag |
| PostgreSQL unavailable | `/readyz` = 503; in-flight requests may finish |
| Usage queue full | record dropped, metric increments |
| Client cancels | upstream stream closes, breaker unaffected |

## Scaling

- Horizontal: multiple instances behind a load balancer; shared control plane
  (PostgreSQL), shared limits and cache (Redis). Each replica periodically
  reloads the control plane and listens to the `gogate:control:reload` Redis channel.
- Redis Cluster needs a different key schema: current Lua scripts assume a single
  Redis instance.
- Public listener is stateless; state lives only in PostgreSQL/Redis.

## Non-goals

Model training, RAG/vector DB as a product, billing, PCI, Kubernetes,
multi-region. Semantic cache is the only pgvector usage.
