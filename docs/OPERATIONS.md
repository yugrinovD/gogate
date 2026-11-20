# Эксплуатация

[English](OPERATIONS.en.md)

## Запуск

### Docker Compose (рекомендуется)

```bash
make init-env          # один раз: .env со случайными секретами
make demo              # mock + Postgres + Redis + Prometheus + Grafana + Jaeger
make demo-ollama       # плюс Ollama и qwen2.5:3b
make down              # остановить
```

Порты на хосте задаются в `.env`: `GOGATE_HTTP_PORT`, `GOGATE_ADMIN_PORT`,
`GOGATE_PROM_PORT`, `GOGATE_GRAFANA_PORT`, `GOGATE_JAEGER_PORT`,
`GOGATE_POSTGRES_PORT`, `GOGATE_REDIS_PORT`.

### Локально

Нужны PostgreSQL 17 (с pgvector, если нужен semantic cache) и Redis 7.
Экспортируйте переменные окружения (минимум `GOGATE_API_KEY_PEPPER`,
`GOGATE_DATABASE_URL`, `GOGATE_REDIS_ADDR`) и запустите:

```bash
go run ./cmd/gogate
```

## Миграции

- SQL-миграции встроены в бинарник (`internal/store/migrations`).
- Применяются на старте под PostgreSQL advisory lock.
- Идемпотентны: повторный запуск пропускает уже применённые версии.
- Отдельной команды миграции нет: сервис мигрирует схему до открытия listener'ов.
- Перед обновлением продакшена делайте backup БД.

## Конфигурация

Полный список — [.env.example](../.env.example). Ключевые группы:

| Группа | Переменные |
| --- | --- |
| Listeners | `GOGATE_PUBLIC_ADDR`, `GOGATE_ADMIN_ADDR`, `GOGATE_ADMIN_TOKEN` |
| Secrets | `GOGATE_API_KEY_PEPPER`, upstream keys через `*_API_KEY` |
| Storage | `GOGATE_DATABASE_URL`, `GOGATE_REDIS_*` |
| Upstream env | `GOGATE_UPSTREAM_KEY_PREFIX`, `GOGATE_OPENAI_KEY_ENV` |
| Limits | `GOGATE_MAX_BODY_BYTES`, `GOGATE_MAX_OUTPUT_TOKENS`, `GOGATE_MAX_MESSAGES`, `GOGATE_MAX_INFLIGHT`, `GOGATE_MAX_API_KEY_BYTES` |
| Timeouts | `GOGATE_REQUEST_TIMEOUT`, `GOGATE_FIRST_BYTE_TIMEOUT`, `GOGATE_STREAM_IDLE_TIMEOUT` |
| Replicas | `GOGATE_CONTROL_RELOAD_INTERVAL` |
| Cache | `GOGATE_CACHE_*`, `GOGATE_SEMANTIC_CACHE_*`, `GOGATE_EMBEDDING_*` |
| Limits/P2 | `GOGATE_RATE_LIMIT_*`, `GOGATE_PII_REDACT`, `GOGATE_BUDGET_ENABLED`, `GOGATE_AUDIT_*` |
| Observability | `GOGATE_METRICS_ENABLED`, `GOGATE_TRACING_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `GOGATE_LOG_LEVEL`, `GOGATE_ACCESS_LOG_SAMPLE` |

Секреты не коммитятся. `.env` создаётся `make init-env` и исключён из git.

## Admin-операции

```bash
AUTH="Authorization: Bearer $GOGATE_ADMIN_TOKEN"

# health
curl -s localhost:9090/admin/v1/health -H "$AUTH" | jq

# применить изменения провайдеров/ключей/квот без рестарта
curl -s -X POST localhost:9090/admin/v1/reload -H "$AUTH" | jq

# проверить провайдера
curl -s -X POST localhost:9090/admin/v1/providers/<id>/test -H "$AUTH" | jq

# usage за 7 дней
curl -s 'localhost:9090/admin/v1/usage?from=2026-11-01&to=2026-11-30' -H "$AUTH" | jq

# очистить semantic cache
curl -s -X POST localhost:9090/admin/v1/cache/purge -H "$AUTH" \
  -H 'Content-Type: application/json' -d '{"exact":true}' | jq
```

Мутации автоматически перестраивают in-memory snapshot и публикуют уведомление
в Redis. Каждая реплика также периодически перечитывает control plane
(`GOGATE_CONTROL_RELOAD_INTERVAL`, по умолчанию 30s; `0` отключает periodic
reload), поэтому отзыв ключа применяется на всех репликах в пределах интервала
или сразу через pub/sub. `POST /reload` обновляет только уже существующие
строки провайдеров. Если строки не было (например, bootstrap был пропущен),
создайте провайдера через admin API; перезапуск bootstrap-ит только пустую
таблицу `providers`.

## Hot reload и Ollama

Если `GOGATE_OLLAMA_BASE_URL` задан, провайдер `ollama` и его модели
регистрируются даже до запуска контейнера. В этом случае `qwen2.5:3b` видна в
`/v1/models`, но запросы к ней возвращают `502 upstream_error`, пока Ollama не
запущена. Если строки провайдера вообще не было (например, bootstrap пропущен
при `GOGATE_ALLOW_PRIVATE_UPSTREAMS=false`), создайте её через admin API:
`POST /reload` не добавляет новые строки. `make demo` не гарантирует работу
`qwen2.5:3b`; для неё используйте `make demo-ollama` или `make llm`.

## Цены и бюджеты

Для mock и Ollama при bootstrap создаётся нулевая цена. Для остальных моделей
цена задаётся через `POST /admin/v1/prices`. Если у тенанта есть
`monthly_cost_usd`, а цены для модели нет, запрос получает
`503 pricing_unavailable`, а метрика `gogate_pricing_missing_total` растёт.
Это защищает от неконтролируемого расхода.

## Квоты

`null`/отсутствие поля означает отсутствие лимита. `0` для `rpm`, `tpm`,
`daily_*` и `monthly_cost_usd` отклоняется. Старые нулевые значения
нормализуются миграцией в `NULL`.

## Retention

Раз в час gateway:

- удаляет истёкшие записи `semantic_cache`;
- удаляет `request_audit` старше `GOGATE_AUDIT_RETENTION` (по умолчанию 30 дней).

Usage (`usage_daily`) не удаляется автоматически — это аналитические данные.
Настройте внешний retention/партиционирование при необходимости.

## Наблюдаемость и SLO

- Prometheus: `http://localhost:9092`
- Grafana: `http://localhost:3000`
- Jaeger: `http://localhost:16686`
- Метрики: `/metrics`; readiness: `/readyz?verbose=1`.

Ориентиры:

- 5xx rate < 1%;
- p99 latency зависит от провайдера, overhead шлюза — единицы миллисекунд;
- `gogate_usage_dropped_total` и `gogate_audit_dropped_total` должны быть 0;
- `gogate_circuit_breaker_state` > 0 указывает на деградацию провайдера.

Alert rules: `deploy/prometheus/alerts.yml`.

## Отладка

- `GOGATE_LOG_LEVEL=debug` — подробные структурные логи.
- `GOGATE_ACCESS_LOG_SAMPLE=0.01` — снизить объём access-логов под нагрузкой.
- `GOGATE_PPROF_ENABLED=true` — pprof на admin-порту (`/debug/pprof/`),
  только с admin-токеном.
- Ошибки upstream логируются без prompt/response; тело upstream-ошибки
  ограничено по размеру.

## Сброс локального состояния

```bash
make down          # остановит контейнеры, volumes сохранятся
make clean         # остановит контейнеры, удалит volumes и build/, bin/; .env не трогает
rm .env            # затем make init-env
```

## Диагностика

| Симптом | Проверка |
| --- | --- |
| `/readyz` 503 | `docker compose logs postgres redis gogate`; проверить DSN/пароль |
| 401 на публичном API | ключ из `build/demo-key.txt`, формат `gk_<env>_<hex>_<hex>` |
| 404 model_not_found | `GET /v1/models`; провайдер enabled и модель в его списке |
| 429 сразу | квоты ключа/тенанта; `Retry-After`; Redis Lua |
| 503 all_providers_unavailable | breaker'ы: `GET /admin/v1/health`; провайдеры живы? |
| Пустые трейсы | `GOGATE_TRACING_ENABLED=true`, `OTEL_EXPORTER_OTLP_ENDPOINT` |
| Нет метрик | `GOGATE_METRICS_ENABLED=true`, scrape target в Prometheus |

## Backup

- PostgreSQL: стандартный `pg_dump`/PITR. Ключи и конфигурация важны; usage/audit
  можно восстанавливать отдельно.
- Redis: не источник истины. Потеря Redis означает сброс лимитов/кэша, но не
  конфигурации.
- `.env` содержит секреты; храните в менеджере секретов, не в git.

## Обновление

- `GOGATE_OPENAI_KEY_ENV` должен указывать на переменную с префиксом
  `GOGATE_UPSTREAM_` (например, `GOGATE_UPSTREAM_OPENAI_API_KEY`). Старое
  `GOGATE_OPENAI_API_KEY` временно принимается с deprecation warning; обновите
  и `.env`, и `providers.api_key_env` (или пересоздайте провайдера).
- `GOGATE_MAX_API_KEY_BYTES` должен быть не меньше минимальной длины ключа для
  вашего `GOGATE_ENV`; при `dev` это 84 байта, безопасный минимум — 128.
- При `GOGATE_OLLAMA_BASE_URL` модель может быть видна в `/v1/models` до
  запуска Ollama; запросы начнут работать после `make llm`/`make demo-ollama`.
