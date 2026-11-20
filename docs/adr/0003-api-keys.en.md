# ADR 0003: API keys

## Status

Accepted.

## Context

A key must be unpredictable, lookupable without storing plaintext and safe for
logs. The hot path needs a fast lookup and keys must be revocable.

## Decision

Format: `gk_<env>_<prefix_hex>_<secret_hex>`.

- prefix — 6 random bytes in hex, stored openly and indexed;
- secret — 32 random bytes in hex;
- the database stores `SHA-256(pepper || secret)`, compared in constant time;
- pepper is required and comes from env;
- plaintext is returned only in the create-key response.

Hex is used instead of base64url so the `_` separator never appears inside
segments and key parsing is unambiguous.

## Consequences

- Prefix lookup is O(1) in the snapshot/database.
- A database leak does not yield usable keys without the pepper.
- Rotating the pepper invalidates all keys; plan a controlled migration.
- Logs may contain only `key_prefix`.
