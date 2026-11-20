DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS vector;
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'pgvector extension is not available: %', SQLERRM;
END $$;

CREATE TABLE IF NOT EXISTS tenants (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name         text NOT NULL,
    key_prefix   text NOT NULL UNIQUE,
    key_hash     bytea NOT NULL,
    enabled      boolean NOT NULL DEFAULT true,
    expires_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS api_keys_tenant_idx ON api_keys(tenant_id);

CREATE TABLE IF NOT EXISTS providers (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE,
    kind         text NOT NULL CHECK (kind IN ('mock', 'ollama', 'openai_compatible')),
    base_url     text NOT NULL DEFAULT '',
    api_key_env  text NOT NULL DEFAULT '',
    models       text[] NOT NULL DEFAULT '{}',
    priority     integer NOT NULL DEFAULT 100,
    enabled      boolean NOT NULL DEFAULT true,
    timeout_ms   integer NOT NULL DEFAULT 30000,
    max_retries  integer NOT NULL DEFAULT 1,
    config       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS quotas (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    api_key_id        uuid REFERENCES api_keys(id) ON DELETE CASCADE,
    rpm               integer,
    tpm               integer,
    daily_requests    bigint,
    daily_tokens      bigint,
    monthly_cost_usd  numeric(14, 6),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS quotas_scope_uidx
    ON quotas(tenant_id, COALESCE(api_key_id, '00000000-0000-0000-0000-000000000000'::uuid));

CREATE TABLE IF NOT EXISTS model_prices (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider              text NOT NULL,
    model                 text NOT NULL,
    input_usd_per_1m      numeric(14, 6) NOT NULL DEFAULT 0,
    output_usd_per_1m     numeric(14, 6) NOT NULL DEFAULT 0,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, model)
);
