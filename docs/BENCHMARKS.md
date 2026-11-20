# Benchmarks

[English](BENCHMARKS.en.md)

## Методика

- Инструмент: k6 0.56.0 в Docker, сценарий `constant-arrival-rate`.
- Нагрузка: 500 RPS, 60 секунд, non-streaming `POST /v1/chat/completions`.
- Провайдер: встроенный `mock`, `max_tokens=32`.
- Exact cache намеренно обходится: каждый запрос содержит уникальный prompt
  (`hello from k6 <VU>-<ITER>`), поэтому измеряется полный путь
  auth → limits → orchestrator → provider → usage, а не попадание в Redis.
- Отдельный baseline: тот же сценарий напрямую в `gogate-mockupstream` (без
  шлюза), чтобы оценить overhead.
- Ошибки: k6 `http_req_failed`.
- RSS: `docker stats` во время прогона.

### Окружение

| Компонент | Значение |
| --- | --- |
| CPU | AMD Ryzen 7 5700X (8C/16T) |
| RAM | 31 GiB |
| OS | Debian 13 (trixie) |
| Docker | 29.10 |
| Go | 1.27.4 |
| PostgreSQL | 17 + pgvector |
| Redis | 7 |

## Результат gateway, 500 RPS

```text
requests: 30 001
rps:      500.0
errors:   0%
p50:      0.43 ms
p95:      0.53 ms
p99:      0.71 ms
max:      43.0 ms
```

## Baseline и overhead

| Сценарий | p50 | p95 | p99 | max |
| --- | --- | --- | --- | --- |
| gogate (500 RPS, 60s) | 0.43 ms | 0.53 ms | 0.71 ms | 43.0 ms |
| mock-upstream напрямую (500 RPS, 30s) | 0.12 ms | 0.15 ms | 0.21 ms | 30.7 ms |

Overhead p99 ≈ **0.5 ms**, что существенно ниже цели в 50 ms. Max включает
редкие выбросы планировщика/GC и не является типичной latency.

## Память

`gogate` RSS под нагрузкой 500 RPS: примерно **60 MiB**; после окончания
нагрузки — около 53 MiB. PostgreSQL ≈ 40 MiB, Redis ≈ 10 MiB (пустые кэши).

## Go-бенчмарки

`make bench`:

```text
BenchmarkKey-16            770484    1377 ns/op    952 B/op   11 allocs/op
BenchmarkMockEmbedder-16   690471    1600 ns/op   3200 B/op    2 allocs/op
BenchmarkHashSecret-16   11182098     107.6 ns/op    32 B/op    1 allocs/op
BenchmarkGenerateKey-16   2722834     439.9 ns/op   352 B/op    7 allocs/op
```

Это микробенчмарки отдельных операций, а не пропускная способность шлюза.

## Как воспроизвести

```bash
make demo          # или make compose-up
make load          # 500 RPS, 2 минуты по умолчанию
# короткий прогон:
K6_DURATION=30s K6_RATE=500 make load
```

Отчёт: `build/k6-summary.json`.

## Что не измерялось

- Реальные LLM-провайдеры: их latency доминирует и зависит от модели/железа.
- Streaming throughput: сценарий сфокусирован на non-streaming overhead.
- Redis Cluster, multi-region, длительные soak-тесты (часы).
