CREATE TABLE IF NOT EXISTS usage_daily (
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    api_key_id        uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    provider          text NOT NULL,
    model             text NOT NULL,
    day               date NOT NULL,
    requests          bigint NOT NULL DEFAULT 0,
    prompt_tokens     bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cost_usd          numeric(14, 6) NOT NULL DEFAULT 0,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, api_key_id, provider, model, day)
);

CREATE INDEX IF NOT EXISTS usage_daily_day_idx ON usage_daily(day);
CREATE INDEX IF NOT EXISTS usage_daily_tenant_day_idx ON usage_daily(tenant_id, day);
