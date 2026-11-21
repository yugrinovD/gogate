# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/).

## [0.1.0] - 2026-11-21

### Added

- OpenAI-compatible public API: `/v1/chat/completions` (JSON and SSE) and
  `/v1/models`.
- Providers: built-in `mock`, native Ollama, and any OpenAI-compatible upstream.
- Retry, fallback, circuit breaker and timeouts bounded to the first streamed
  byte; no duplicate work after a partial stream.
- API keys with SHA-256 + pepper, one-time plaintext display, RPM/TPM limits,
  daily quotas and monthly cost budgets.
- Exact Redis cache for non-streaming requests and optional pgvector semantic
  cache.
- PostgreSQL control plane for tenants, keys, providers, quotas, prices, usage
  and audit.
- Prometheus metrics, OTLP traces, pprof, structured JSON logs without prompts
  or keys.
- Separate admin listener with its own token and SSRF-safe upstream validation.
- Docker Compose with mock, PostgreSQL, Redis, Prometheus, Grafana, Jaeger and
  an optional standalone Ollama profile.
- k6 load profile, Go benchmarks, ADRs, threat model and OpenAPI 3.1 specs.
