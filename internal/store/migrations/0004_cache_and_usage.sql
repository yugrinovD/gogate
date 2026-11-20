DO $do$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'semantic_cache') THEN
        EXECUTE 'ALTER TABLE semantic_cache ADD COLUMN IF NOT EXISTS params_hash bytea NOT NULL DEFAULT ''\x''';
        EXECUTE 'DROP INDEX IF EXISTS semantic_cache_scope_idx';
        EXECUTE 'CREATE INDEX IF NOT EXISTS semantic_cache_scope_idx ON semantic_cache(tenant_id, model, params_hash, expires_at)';
    END IF;
END $do$;

ALTER TABLE usage_daily DROP CONSTRAINT IF EXISTS usage_daily_tenant_id_fkey;
ALTER TABLE usage_daily DROP CONSTRAINT IF EXISTS usage_daily_api_key_id_fkey;
