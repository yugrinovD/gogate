# ADR 0006: Cache tiers

## Status

Accepted.

## Context

Repeated non-streaming requests can be served faster and cheaper. We need an
exact cache and an optional semantic cache, while streaming cannot be cached
without buffering.

## Decision

- **Exact** (Redis): non-streaming only. Key = tenant, model, messages,
  temperature, max_tokens and other generation parameters. TTL.
- **Semantic** (pgvector): opt-in, non-streaming only. Prompt embedding, cosine
  similarity with a threshold, tenant+model isolation.
- Streaming is always `BYPASS`.
- A cache hit returns the stored usage but does not charge upstream tokens: the
  limit reservation is refunded.

## Consequences

- Redis is required for exact cache; semantic cache requires pgvector and an embedder.
- The key includes extra parameters (top_p/stop/seed) to avoid wrong hits.
- Semantic cache may return a close but not identical response; threshold and
  embedder affect quality.
