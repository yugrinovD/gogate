# ADR 0002: PostgreSQL control plane, Redis hot path

## Status

Accepted.

## Context

Keys, providers and quotas change rarely but are read on every request. Querying
PostgreSQL per chat request adds latency and couples gateway availability to the
database.

## Decision

PostgreSQL is the source of truth for tenants, keys, providers, quotas, prices,
usage, audit and semantic cache. At startup and on admin mutations an immutable
in-memory snapshot is built (`atomic.Pointer`). Auth and routing read only the
snapshot.

Redis is used for limits, quotas and exact cache. It is not a source of truth:
losing Redis resets counters and cache, not configuration.

## Consequences

- The hot path does not depend on PostgreSQL latency.
- Multiple instances share one control plane.
- Changes made directly in the database are picked up by periodic reload
  (`GOGATE_CONTROL_RELOAD_INTERVAL`) or `POST /admin/v1/reload`.
- The snapshot must be thread-safe and atomically replaced.
