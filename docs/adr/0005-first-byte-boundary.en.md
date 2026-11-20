# ADR 0005: First-byte boundary

## Status

Accepted.

## Context

With SSE a request cannot be retried after the client received part of the
response: it would duplicate text and break the contract. Before the first byte,
retry/fallback are safe.

## Decision

`Orchestrator.Open` calls the provider and reads exactly one chunk under the
first-byte timeout. Only then is the stream considered committed and returned to
the caller.

- Before commit: retry with backoff+jitter, fallback to the next provider,
  circuit-breaker failure.
- After commit: errors are only sent as SSE/terminal errors, fallback is
  forbidden, the breaker is not touched.
- Client cancellation is not a provider failure.

## Consequences

- The first chunk is delayed by one `Next`, but the full response is never buffered.
- An explicit first-byte timeout and correct HTTP request cancellation are required.
- Post-first-byte errors are visible to the client as an SSE error.
