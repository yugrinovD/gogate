# Threat model

[Russian](THREAT_MODEL.md)

## Assets

- Client API keys and the admin token.
- Upstream provider keys (env only).
- Prompt and model response content.
- Provider configuration and routing.
- Usage/audit data and tenant budgets.
- Availability of the gateway and upstream providers.

## Trust boundaries

```text
client ──(public, API key)──► gogate ──(admin token, separate port)──► ops
                                  │
                                  ├── PostgreSQL (control plane)
                                  ├── Redis (hot path)
                                  └── upstream providers (admin-configured URLs)
```

- Public clients are untrusted.
- The admin operator is trusted, but actions are validated.
- Providers are external and potentially unstable.
- PostgreSQL/Redis are trusted infrastructure inside the network.

## Threats and mitigations

### API key theft/brute force

- 32 random secret bytes, SHA-256 + pepper, prefix lookup.
- No plaintext in the database; logs contain only `key_prefix`.
- Constant-time hash comparison.
- Keys can be disabled (`enabled=false`) or deleted; the snapshot refreshes.
- `expires_at` is supported.
- RPM/TPM limits reduce brute-force and abuse speed.

Residual risk: a key leaked through a client application. Rotate and use quotas.

### SSRF via provider URL

- Only the admin API sets URLs.
- The public API accepts no URLs.
- Scheme (http/https), missing userinfo, host and DNS resolution are validated.
- Metadata (`169.254.169.254`, `metadata.google.internal`), link-local,
  unspecified and multicast addresses are blocked.
- Private/loopback addresses require `GOGATE_ALLOW_PRIVATE_UPSTREAMS=true`
  (needed for local Ollama).
- The HTTP client does not follow redirects.

Residual risk: DNS rebinding and IP changes after validation. Use an egress
firewall and allowlist in strict environments.

### Prompt/response leakage into logs or audit

- Prompts/responses are not logged.
- Audit stores metadata only.
- Upstream error bodies are size-limited and not logged in full.
- Optional request PII redaction replaces email/phone/card.
- Streaming responses are not redacted (documented limitation).
- Caches store responses: exact in Redis, semantic in PostgreSQL. This is tenant
  data; access to Redis/PostgreSQL must be network-restricted.

### Limit bypass

- Limits are checked atomically in Redis Lua for tenant and key scopes.
- RPM/TPM are reserved before the request and settled afterwards.
- Daily and monthly limits use separate counters.
- If Redis is unavailable, the default is fail-closed.
- Keys and tenants are isolated.

Residual risk: explicitly enabled fail-open; budgets use soft settlement.

### Admin surface compromise

- Separate port, router and token.
- Constant-time token comparison.
- The public listener has no admin routes.
- pprof is flag-gated and admin-only.
- Config is returned without secrets.
- Provider secrets are env-only; the database stores the variable name.

Recommendation: never expose the admin port to the internet; use VPN/bastion.

### DoS

- Body limit, message/token limits, read/write timeouts.
- Configurable `GOGATE_MAX_INFLIGHT` and an inflight metric.
- Per-key/tenant rate limits.
- Bounded usage/audit queues; overflow does not block the hot path.
- Size limits on SSE/JSON chunks and upstream error bodies.
- Circuit breaker prevents sticking to a failing provider.

Residual risk: distributed DDoS at the load balancer; use edge protection.

### Injection/invalid input

- JSON decoding is body-limited; unknown fields are ignored.
- The model must exist in the control plane; arbitrary upstream URLs are impossible.
- SQL uses parameterized pgx queries only.
- Lua scripts receive data via ARGV; keys are built from internal IDs.
- Message roles and content types are validated.

### Secrets in the repository

- `.env` is generated locally and git-ignored.
- `.env.example` contains placeholders only.
- CI: gitleaks, govulncheck.
- Provider keys are not stored in the database.

## Residual risks

- TLS terminates at an external reverse proxy; without it traffic is unencrypted.
- Redis/PostgreSQL must be on a private network.
- Semantic cache uses embeddings; quality and privacy depend on the embedder.
- Prompt injection is the application/model's responsibility, not the gateway's.
- Logs and metrics may contain tenant/model/provider; not secrets, but apply a
  retention policy.
