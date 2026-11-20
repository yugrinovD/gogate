# ADR 0007: Async usage and cost

## Status

Accepted.

## Context

Writing usage/audit to PostgreSQL on every request adds latency and database load
at 500+ RPS. Losing a small number of records on overflow is acceptable if it is
visible in metrics.

## Decision

Use bounded queues and a background writer:

- usage records are merged by tenant/key/provider/model/day and written in batches;
- audit is written in batches without conversation content;
- flush by batch size or timer;
- on queue overflow the record is dropped and a metric is incremented;
- graceful shutdown drains queues and performs a final flush.

Cost is computed from `model_prices` before writing.

## Consequences

- The hot path is not blocked on PostgreSQL.
- Usage/audit may lag by seconds.
- Drops are observable and are not retried indefinitely.
- Shutdown ordering matters.
