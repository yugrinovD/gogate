# ADR 0001: Provider abstraction

## Status

Accepted.

## Context

The gateway needs mock, Ollama and arbitrary OpenAI-compatible upstreams. They
differ in stream format, usage fields and authentication.

## Decision

Define a common `Provider` interface with `Chat(ctx, Request) (Stream, error)`.
Each adapter normalizes responses into a common `Chunk` (role, content,
finish_reason, usage). The public HTTP layer only sees normalized chunks and
OpenAI-compatible DTOs.

The registry builds fallback chains by model and priority. Providers are created
from the control plane; upstream secrets are read from env by variable name.

## Consequences

- A new provider is one adapter plus a database row.
- Streaming/usage/fallback are implemented once in the orchestrator.
- Error normalization (`UpstreamError`) and usage must be maintained.
- There is no raw pass-through: fields outside the common contract are dropped.
