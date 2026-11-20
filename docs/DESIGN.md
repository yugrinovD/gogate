# Архитектура gogate

[English](DESIGN.en.md)

## Цели

- Единый OpenAI-совместимый API поверх нескольких провайдеров.
- Локальный запуск без внешних ключей: mock по умолчанию, Ollama по требованию.
- Предсказуемое поведение под нагрузкой: ограниченная память, асинхронные записи,
  отсутствие походов в PostgreSQL на горячем пути.
- Наблюдаемость и безопасность по умолчанию: метрики, трейсы, JSON-логи без
  содержимого диалога, отдельный admin-контур.
- Чёткая граница отказоустойчивости: retry/fallback/breaker только до первого
  байта стрима.

## Слои

```text
HTTP (public/admin)
  → middleware: request-id, recover, body limit, CORS, access log
  → auth: API key или admin token
  → rate limit / quotas (Redis Lua)
  → cache (Redis exact, pgvector semantic)
  → orchestrator (retry, fallback, breaker, timeouts)
  → provider adapter (mock / ollama / openai-compatible)
  → async usage/audit writer → PostgreSQL
```

### Public listener

`POST /v1/chat/completions`, `GET /v1/models`, `/healthz`, `/readyz`, `/metrics`.

Порядок обработки запроса:

1. Проверка Content-Length/body limit.
2. `Authorization: Bearer gk_...`: разбор префикса, поиск в in-memory snapshot,
   проверка enabled/expiry и `SHA-256(pepper || secret)` в constant time.
3. Валидация OpenAI-полей: модель, сообщения, temperature, top_p, max_tokens,
   stop, n=1.
4. PII-redaction запроса (если включена).
5. Проверка, что модель существует у включённого провайдера.
6. Атомарная проверка RPM/TPM/дневных квот/бюджета в Redis; резерв токенов и
   стоимости; при отказе — `429` + `Retry-After`.
7. Exact cache (только non-streaming). При попадании резерв возвращается.
8. Semantic cache (опционально) по embedding.
9. Orchestrator открывает поток и подтверждает первый чанк.
10. Streaming: SSE без буферизации; non-streaming: агрегация чанков.
11. Settlement фактических токенов/стоимости, async usage/audit, метрики, лог.

### Admin listener

Отдельный порт и токен. Даёт CRUD над тенантами, ключами, провайдерами, квотами
и ценами, health, usage/audit, purge кэша, hot reload и pprof по флагу.
Мутации вызывают перестройку in-memory snapshot.

## Control plane и hot path

- **PostgreSQL** — источник истины: ключи (hash), провайдеры, квоты, цены,
  usage, audit, semantic cache.
- **In-memory snapshot** (`atomic.Pointer`) — ключи, провайдеры, маршруты
  model → providers, квоты, цены. Обновляется при admin-мутациях и вручную.
- **Redis** — лимиты, квоты, exact cache. Не является источником истины.
- **Usage/audit** — батчевый writer с ограниченными очередями; при переполнении
  записи дропаются и инкрементируют метрику.

Такое разделение убирает PostgreSQL из горячего пути и позволяет переживать
рестарты без потери конфигурации.

## Провайдеры

```go
type Provider interface {
    Name() string
    Kind() string
    Models() []string
    Chat(ctx context.Context, req Request) (Stream, error)
    Ping(ctx context.Context) error
}
```

- `mock` — детерминированные ответы, чанки, usage; умеет инжектировать ошибки и
  задержки для тестов.
- `ollama` — native `/api/chat`, NDJSON → нормализованные чанки.
- `openai_compatible` — `/chat/completions`, SSE, Bearer из env-переменной.

Registry строит цепочку fallback: для каждой модели список включённых
провайдеров сортируется по `priority`.

## Граница первого байта

`Orchestrator.Open`:

1. Берёт цепочку провайдеров.
2. Для каждого: retry с backoff+jitter, пока не исчерпан `max_retries`.
3. Вызывает `Chat`, затем ровно один `Next` с таймаутом первого байта.
4. Если первый чанк получен — поток «закоммичен», fallback запрещён.
5. Ошибки до первого байта считаются failure для breaker; после — только
   инкрементируют upstream error metric.

Это защищает клиента от дублирования уже отправленного текста и делает
поведение предсказуемым.

## Rate limit и квоты

Redis Lua, один атомарный вызов на запрос:

- RPM и TPM — token bucket/GCRA на scope tenant и key;
- дневные запросы/токены — счётчики с TTL;
- месячная стоимость — `INCRBYFLOAT` с TTL;
- резерв `prompt + max_tokens` и оценка стоимости, затем settlement по факту.

При недоступности Redis по умолчанию fail-closed; `GOGATE_RATE_LIMIT_FAIL_OPEN`
меняет поведение.

## Кэш

- **Exact**: Redis, только non-streaming. Ключ — tenant + model + messages +
  параметры генерации. TTL настраивается.
- **Semantic**: pgvector, opt-in, только non-streaming. Embedding промпта,
  cosine similarity, порог, изоляция по tenant+model.
- Streaming никогда не кэшируется.
- Кэш-хиты не расходуют upstream-токены: резерв лимитов возвращается.

## Usage, cost, audit

- `usage_daily` — upsert по tenant/key/provider/model/day.
- Cost считается по `model_prices` (USD за 1M токенов).
- `request_audit` хранит только метаданные: request_id, tenant, prefix, model,
  provider, status, cache, tokens, cost, latency, error, PII flag.
- Retention job удаляет старые audit и истёкший semantic cache.

## Отказы и деградация

| Отказ | Поведение |
| --- | --- |
| Провайдер вернул 5xx/timeout до первого байта | retry, затем fallback |
| Провайдер упал после первого байта | SSE error, без fallback |
| Все breaker'ы открыты | `503 all_providers_unavailable` |
| Redis недоступен | fail-closed `503` или fail-open по флагу |
| PostgreSQL недоступен | `/readyz` = 503; уже открытые запросы могут завершиться |
| Очередь usage переполнена | запись дропается, метрика растёт |
| Клиент отменил запрос | upstream stream закрывается, breaker не страдает |

## Масштабирование

- Горизонтальное: несколько экземпляров за балансировщиком; control plane
  общий (PostgreSQL), лимиты и кэш общие (Redis). Каждая реплика периодически
  перечитывает control plane и слушает Redis pub/sub `gogate:control:reload`.
- Для Redis Cluster потребуется отдельная схема ключей: текущие Lua-скрипты
  рассчитаны на один Redis.
- Stateless public listener, состояние — только в PostgreSQL/Redis.

## Non-goals

Обучение моделей, RAG/vector DB как продукт, биллинг, PCI, Kubernetes,
multi-region. Semantic cache — единственное использование pgvector.
