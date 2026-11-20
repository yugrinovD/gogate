CREATE TABLE IF NOT EXISTS request_audit (
    id                bigserial PRIMARY KEY,
    request_id        text NOT NULL,
    tenant_id         uuid NOT NULL,
    key_prefix        text NOT NULL,
    model             text NOT NULL,
    provider          text NOT NULL DEFAULT '',
    stream            boolean NOT NULL DEFAULT false,
    status            integer NOT NULL,
    cache             text NOT NULL DEFAULT '',
    prompt_tokens     integer NOT NULL DEFAULT 0,
    completion_tokens integer NOT NULL DEFAULT 0,
    cost_usd          numeric(14, 6) NOT NULL DEFAULT 0,
    latency_ms        integer NOT NULL DEFAULT 0,
    error_code        text NOT NULL DEFAULT '',
    pii_redacted      boolean NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS request_audit_tenant_created_idx ON request_audit(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS request_audit_created_idx ON request_audit(created_at DESC);

DO $do$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') THEN
        EXECUTE $ddl$
            CREATE TABLE IF NOT EXISTS semantic_cache (
                id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
                model       text NOT NULL,
                prompt_hash bytea NOT NULL,
                params_hash bytea NOT NULL DEFAULT '\x',
                provider    text NOT NULL DEFAULT '',
                embedding   vector(768) NOT NULL,
                response    jsonb NOT NULL,
                usage       jsonb NOT NULL DEFAULT '{}'::jsonb,
                cost_usd    numeric(14, 6) NOT NULL DEFAULT 0,
                expires_at  timestamptz NOT NULL,
                created_at  timestamptz NOT NULL DEFAULT now()
            )
        $ddl$;
        EXECUTE $ddl$
            CREATE INDEX IF NOT EXISTS semantic_cache_scope_idx
                ON semantic_cache(tenant_id, model, params_hash, expires_at)
        $ddl$;
        EXECUTE $ddl$
            CREATE INDEX IF NOT EXISTS semantic_cache_embedding_idx
                ON semantic_cache USING hnsw (embedding vector_cosine_ops)
        $ddl$;
    ELSE
        RAISE NOTICE 'pgvector extension is absent; semantic cache table skipped';
    END IF;
END $do$;
