# Operations

[Russian](OPERATIONS.md)

## Running

### Docker Compose (recommended)

```bash
make init-env          # once: .env with random secrets
make demo              # mock + Postgres + Redis + Prometheus + Grafana + Jaeger
make demo-ollama       # plus Ollama and qwen2.5:3b
make down              # stop
```

Host ports are configured in `.env`: `GOGATE_HTTP_PORT`, `GOGATE_ADMIN_PORT`,
`GOGATE_PROM_PORT`, `GOGATE_GRAFANA_PORT`, `GOGATE_JAEGER_PORT`,
`GOGATE_POSTGRES_PORT`, `GOGATE_REDIS_PORT`.

### Locally

Requires PostgreSQL 17 (with pgvector for semantic cache) and Redis 7. Export the
environment (at least `GOGATE_API_KEY_PEPPER`, `GOGATE_DATABASE_URL`,
`GOGATE_REDIS_ADDR`) and run:

```bash
go run ./cmd/gogate
```

## Migrations

- SQL migrations are embedded in the binary (`internal/store/migrations`).
- They run at startup under a PostgreSQL advisory lock.
- They are idempotent: already applied versions are skipped.
- There is no separate migration command: the service migrates before opening listeners.
- Back up the database before production upgrades.

## Configuration

Full list: [.env.example](../.env.example). Main groups:

| Group | Variables |
| --- | --- |
| Listeners | `GOGATE_PUBLIC_ADDR`, `GOGATE_ADMIN_ADDR`, `GOGATE_ADMIN_TOKEN` |
| Secrets | `GOGATE_API_KEY_PEPPER`, upstream keys via `*_API_KEY` |
| Storage | `GOGATE_DATABASE_URL`, `GOGATE_REDIS_*` |
| Upstream env | `GOGATE_UPSTREAM_KEY_PREFIX`, `GOGATE_OPENAI_KEY_ENV` |
| Limits | `GOGATE_MAX_BODY_BYTES`, `GOGATE_MAX_OUTPUT_TOKENS`, `GOGATE_MAX_MESSAGES`, `GOGATE_MAX_INFLIGHT`, `GOGATE_MAX_API_KEY_BYTES` |
| Timeouts | `GOGATE_REQUEST_TIMEOUT`, `GOGATE_FIRST_BYTE_TIMEOUT`, `GOGATE_STREAM_IDLE_TIMEOUT` |
| Replicas | `GOGATE_CONTROL_RELOAD_INTERVAL` |
| Cache | `GOGATE_CACHE_*`, `GOGATE_SEMANTIC_CACHE_*`, `GOGATE_EMBEDDING_*` |
| Limits/P2 | `GOGATE_RATE_LIMIT_*`, `GOGATE_PII_REDACT`, `GOGATE_BUDGET_ENABLED`, `GOGATE_AUDIT_*` |
| Observability | `GOGATE_METRICS_ENABLED`, `GOGATE_TRACING_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `GOGATE_LOG_LEVEL`, `GOGATE_ACCESS_LOG_SAMPLE` |

Secrets are never committed. `.env` is created by `make init-env` and git-ignored.

## Admin operations

```bash
AUTH="Authorization: Bearer $GOGATE_ADMIN_TOKEN"

# health
curl -s localhost:9090/admin/v1/health -H "$AUTH" | jq

# apply provider/key/quota changes without a restart
curl -s -X POST localhost:9090/admin/v1/reload -H "$AUTH" | jq

# check a provider
curl -s -X POST localhost:9090/admin/v1/providers/<id>/test -H "$AUTH" | jq

# usage for 7 days
curl -s 'localhost:9090/admin/v1/usage?from=2026-11-01&to=2026-11-30' -H "$AUTH" | jq

# purge semantic cache
curl -s -X POST localhost:9090/admin/v1/cache/purge -H "$AUTH" \
  -H 'Content-Type: application/json' -d '{"exact":true}' | jq
```

Mutations rebuild the in-memory snapshot and publish a Redis notification. Each
replica also reloads the control plane periodically
(`GOGATE_CONTROL_RELOAD_INTERVAL`, 30s by default), so key revocation reaches all
replicas within that interval. `POST /reload` is needed when a provider
appears/disappears in DNS (for example, the Ollama container was started after
gogate) or when the control plane was changed directly in the database.

## Hot reload and Ollama

`GOGATE_OLLAMA_BASE_URL` may point to a container that does not exist yet. At
startup such a provider is skipped with a warning. After `make llm` or
`make demo-ollama`, call `POST /admin/v1/reload` and the provider appears in
`/v1/models`.

## Retention

Once an hour the gateway:

- deletes expired `semantic_cache` rows;
- deletes `request_audit` rows older than `GOGATE_AUDIT_RETENTION` (30 days by default).

Usage (`usage_daily`) is not deleted automatically; it is analytical data. Add
external retention/partitioning if needed.

## Observability and SLOs

- Prometheus: `http://localhost:9092`
- Grafana: `http://localhost:3000`
- Jaeger: `http://localhost:16686`
- Metrics: `/metrics`; readiness: `/readyz?verbose=1`.

Guidelines:

- 5xx rate < 1%;
- p99 latency depends on the provider; gateway overhead is a few milliseconds;
- `gogate_usage_dropped_total` and `gogate_audit_dropped_total` should be 0;
- `gogate_circuit_breaker_state` > 0 indicates provider degradation.

Alert rules: `deploy/prometheus/alerts.yml`.

## Debugging

- `GOGATE_LOG_LEVEL=debug` — detailed structured logs.
- `GOGATE_ACCESS_LOG_SAMPLE=0.01` — reduce access-log volume under load.
- `GOGATE_PPROF_ENABLED=true` — pprof on the admin port (`/debug/pprof/`), admin
  token required.
- Upstream errors are logged without prompts/responses; upstream error bodies are
  size-limited.

## Resetting local state

```bash
make down          # stops containers, keeps volumes
make clean         # stops containers, removes volumes and build/, bin/; keeps .env
rm .env            # then make init-env
```

## Troubleshooting

| Symptom | Check |
| --- | --- |
| `/readyz` 503 | `docker compose logs postgres redis gogate`; DSN/password |
| 401 on public API | key from `build/demo-key.txt`, format `gk_<env>_<hex>_<hex>` |
| 404 model_not_found | `GET /v1/models`; provider enabled and model listed |
| Immediate 429 | key/tenant quotas; `Retry-After`; Redis Lua |
| 503 all_providers_unavailable | breakers: `GET /admin/v1/health`; are providers alive? |
| Empty traces | `GOGATE_TRACING_ENABLED=true`, `OTEL_EXPORTER_OTLP_ENDPOINT` |
| No metrics | `GOGATE_METRICS_ENABLED=true`, Prometheus scrape target |

## Backup

- PostgreSQL: standard `pg_dump`/PITR. Keys and configuration matter; usage/audit
  can be restored separately.
- Redis: not a source of truth. Losing Redis resets limits/cache, not configuration.
- `.env` contains secrets; keep it in a secret manager, never in git.
