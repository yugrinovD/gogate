# Benchmarks

[Russian](BENCHMARKS.md)

## Methodology

- Tool: k6 0.56.0 in Docker, `constant-arrival-rate` scenario.
- Load: 500 RPS for 60 seconds, non-streaming `POST /v1/chat/completions`.
- Provider: built-in `mock`, `max_tokens=32`.
- Exact cache is deliberately bypassed: every request carries a unique prompt
  (`hello from k6 <VU>-<ITER>`), so the measured path is the full
  auth → limits → orchestrator → provider → usage path, not a Redis hit.
- Baseline: the same scenario against `gogate-mockupstream` directly (no gateway)
  to estimate overhead.
- Errors: k6 `http_req_failed`.
- RSS: `docker stats` during the run.

### Environment

| Component | Value |
| --- | --- |
| CPU | AMD Ryzen 7 5700X (8C/16T) |
| RAM | 31 GiB |
| OS | Debian 13 (trixie) |
| Docker | 29.10 |
| Go | 1.27.4 |
| PostgreSQL | 17 + pgvector |
| Redis | 7 |

## Gateway result, 500 RPS

```text
requests: 30,001
rps:      500.0
errors:   0%
p50:      0.44 ms
p95:      0.54 ms
p99:      0.75 ms
max:      56.8 ms
```

## Baseline and overhead

| Scenario | p50 | p95 | p99 | max |
| --- | --- | --- | --- | --- |
| gogate (500 RPS, 60s) | 0.44 ms | 0.54 ms | 0.75 ms | 56.8 ms |
| mock upstream directly (500 RPS, 30s) | 0.12 ms | 0.15 ms | 0.21 ms | 30.7 ms |

p99 overhead is ≈ **0.54 ms**, far below the 50 ms target. Max includes rare
scheduler/GC outliers and is not typical latency.

## Memory

`gogate` RSS under 500 RPS: about **60 MiB**; after the run: about 53 MiB.
PostgreSQL ≈ 40 MiB, Redis ≈ 10 MiB (empty caches).

## Go benchmarks

`make bench`:

```text
BenchmarkKey-16            645720    1655 ns/op   1161 B/op   11 allocs/op
BenchmarkMockEmbedder-16   698066    1579 ns/op   3200 B/op    2 allocs/op
BenchmarkHashSecret-16   11187235     107.3 ns/op    32 B/op    1 allocs/op
BenchmarkGenerateKey-16   2756547     438.1 ns/op   352 B/op    7 allocs/op
```

These are microbenchmarks of individual operations, not gateway throughput.

## Reproduce

```bash
make demo          # or make compose-up
make load          # 500 RPS, 2 minutes by default
# short run:
K6_DURATION=30s K6_RATE=500 make load
```

Report: `build/k6-summary.json`.

## Not measured

- Real LLM providers: their latency dominates and depends on model/hardware.
- Streaming throughput: the scenario focuses on non-streaming overhead.
- Redis Cluster, multi-region, multi-hour soak tests.
