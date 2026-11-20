# ADR 0008: Admin isolation and SSRF defence

## Status

Accepted.

## Context

The admin API manages providers and their URLs. If the admin surface is exposed
publicly or URLs are not validated, it opens a path to SSRF and control-plane
compromise.

## Decision

- Separate listener (`GOGATE_ADMIN_ADDR`) and router.
- Separate static Bearer token, constant-time comparison.
- The public listener has no admin routes.
- Only admins set provider URLs.
- URL validation: http/https, no userinfo, DNS resolution, block metadata,
  link-local, multicast, unspecified; private/loopback only behind a flag.
- The HTTP client does not follow redirects.
- pprof is admin-only and flag-gated.
- Provider secrets are env-only; the database stores only the variable name.

## Consequences

- Never expose the admin port to the internet; use VPN/bastion.
- Local Ollama requires `GOGATE_ALLOW_PRIVATE_UPSTREAMS=true`.
- DNS rebinding remains a residual risk; use an egress firewall.
