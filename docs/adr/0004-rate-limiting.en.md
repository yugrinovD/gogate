# ADR 0004: Rate limiting and quotas

## Status

Accepted.

## Context

RPM and TPM per key and tenant, daily quotas, monthly budgets and a correct
`Retry-After` under concurrency are required. Checks must be atomic.

## Decision

One Redis Lua script handles admission:

- RPM/TPM as token buckets/GCRA on tenant and key scopes;
- daily request/token counters with TTL;
- monthly cost as `INCRBYFLOAT` with TTL;
- reserve `prompt + max_tokens` and estimated cost;
- on rejection return the reason and `Retry-After`.

After the response, settlement corrects the reservation using actual usage and
cost. If Redis is unavailable, the default is fail-closed.

## Consequences

- One Redis round-trip per request, atomic across scopes.
- TPM accounts for the reservation, so underestimation may slightly overshoot.
- Budgets are soft at settlement.
- Lua scripts assume a single Redis; a cluster needs a different schema.
