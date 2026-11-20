package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CreateTenant inserts a tenant or returns the existing one with the same name.
func (s *Store) CreateTenant(ctx context.Context, name string) (Tenant, error) {
	var t Tenant
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO tenants(name) VALUES($1)
		ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name
		RETURNING id::text, name, created_at`, name).Scan(&t.ID, &t.Name, &t.CreatedAt)
	return t, err
}

// ListTenants returns all tenants ordered by name.
func (s *Store) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text, name, created_at FROM tenants ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTenant removes a tenant and all dependent rows.
func (s *Store) DeleteTenant(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CreateAPIKey stores a key hash and prefix.
func (s *Store) CreateAPIKey(ctx context.Context, k APIKey) (APIKey, error) {
	var out APIKey
	var expires sql.NullTime
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO api_keys(tenant_id, name, key_prefix, key_hash, enabled, expires_at)
		VALUES($1,$2,$3,$4,$5,$6)
		RETURNING id::text, tenant_id::text, name, key_prefix, key_hash, enabled, expires_at, last_used_at, created_at`,
		k.TenantID, k.Name, k.Prefix, k.Hash, k.Enabled, k.ExpiresAt).
		Scan(&out.ID, &out.TenantID, &out.Name, &out.Prefix, &out.Hash, &out.Enabled, &expires, &out.LastUsedAt, &out.CreatedAt)
	if err != nil {
		return out, err
	}
	if expires.Valid {
		out.ExpiresAt = &expires.Time
	}
	return out, nil
}

// ListAPIKeys returns keys, optionally filtered by tenant.
func (s *Store) ListAPIKeys(ctx context.Context, tenantID string) ([]APIKey, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, tenant_id::text, name, key_prefix, key_hash, enabled, expires_at, last_used_at, created_at
		FROM api_keys
		WHERE ($1 = '' OR tenant_id::text = $1)
		ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var expires sql.NullTime
		if err := rows.Scan(&k.ID, &k.TenantID, &k.Name, &k.Prefix, &k.Hash, &k.Enabled, &expires, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		if expires.Valid {
			k.ExpiresAt = &expires.Time
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetAPIKeyByPrefix looks up a key by its public prefix.
func (s *Store) GetAPIKeyByPrefix(ctx context.Context, prefix string) (APIKey, error) {
	var k APIKey
	var expires sql.NullTime
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, name, key_prefix, key_hash, enabled, expires_at, last_used_at, created_at
		FROM api_keys WHERE key_prefix=$1`, prefix).
		Scan(&k.ID, &k.TenantID, &k.Name, &k.Prefix, &k.Hash, &k.Enabled, &expires, &k.LastUsedAt, &k.CreatedAt)
	if err != nil {
		return k, err
	}
	if expires.Valid {
		k.ExpiresAt = &expires.Time
	}
	return k, nil
}

// SetAPIKeyEnabled enables or disables a key.
func (s *Store) SetAPIKeyEnabled(ctx context.Context, id string, enabled bool) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE api_keys SET enabled=$2 WHERE id=$1`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteAPIKey removes a key.
func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM api_keys WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// TouchAPIKeys updates last_used_at for a batch of keys.
func (s *Store) TouchAPIKeys(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id = ANY($1::uuid[])`, ids)
	return err
}

// CreateProvider inserts a provider.
func (s *Store) CreateProvider(ctx context.Context, p Provider) (Provider, error) {
	if len(p.Config) == 0 {
		p.Config = json.RawMessage(`{}`)
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO providers(name, kind, base_url, api_key_env, models, priority, enabled, timeout_ms, max_retries, config)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id::text, name, kind, base_url, api_key_env, models, priority, enabled, timeout_ms, max_retries, config, created_at, updated_at`,
		p.Name, p.Kind, p.BaseURL, p.APIKeyEnv, p.Models, p.Priority, p.Enabled, p.TimeoutMS, p.MaxRetries, []byte(p.Config))
	return scanProvider(row)
}

// UpdateProvider replaces provider configuration by id.
func (s *Store) UpdateProvider(ctx context.Context, id string, p Provider) (Provider, error) {
	if len(p.Config) == 0 {
		p.Config = json.RawMessage(`{}`)
	}
	row := s.Pool.QueryRow(ctx, `
		UPDATE providers SET name=$2, kind=$3, base_url=$4, api_key_env=$5, models=$6,
			priority=$7, enabled=$8, timeout_ms=$9, max_retries=$10, config=$11, updated_at=now()
		WHERE id=$1
		RETURNING id::text, name, kind, base_url, api_key_env, models, priority, enabled, timeout_ms, max_retries, config, created_at, updated_at`,
		id, p.Name, p.Kind, p.BaseURL, p.APIKeyEnv, p.Models, p.Priority, p.Enabled, p.TimeoutMS, p.MaxRetries, []byte(p.Config))
	return scanProvider(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProvider(row rowScanner) (Provider, error) {
	var p Provider
	var config []byte
	if err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.APIKeyEnv, &p.Models, &p.Priority,
		&p.Enabled, &p.TimeoutMS, &p.MaxRetries, &config, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, err
	}
	if len(config) == 0 {
		config = []byte(`{}`)
	}
	p.Config = json.RawMessage(config)
	return p, nil
}

// GetProvider returns a provider by id.
func (s *Store) GetProvider(ctx context.Context, id string) (Provider, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id::text, name, kind, base_url, api_key_env, models, priority, enabled, timeout_ms, max_retries, config, created_at, updated_at
		FROM providers WHERE id=$1`, id)
	return scanProvider(row)
}

// ListProviders returns providers ordered by priority.
func (s *Store) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, name, kind, base_url, api_key_env, models, priority, enabled, timeout_ms, max_retries, config, created_at, updated_at
		FROM providers ORDER BY priority, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProvider removes a provider.
func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM providers WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpsertQuota creates or replaces a tenant/key quota.
func (s *Store) UpsertQuota(ctx context.Context, q Quota) (Quota, error) {
	var out Quota
	var keyID sql.NullString
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO quotas(tenant_id, api_key_id, rpm, tpm, daily_requests, daily_tokens, monthly_cost_usd)
		VALUES($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7)
		ON CONFLICT (tenant_id, COALESCE(api_key_id, '00000000-0000-0000-0000-000000000000'::uuid))
		DO UPDATE SET rpm=EXCLUDED.rpm, tpm=EXCLUDED.tpm, daily_requests=EXCLUDED.daily_requests,
			daily_tokens=EXCLUDED.daily_tokens, monthly_cost_usd=EXCLUDED.monthly_cost_usd, updated_at=now()
		RETURNING id::text, tenant_id::text, api_key_id::text, rpm, tpm, daily_requests, daily_tokens, monthly_cost_usd::float8, created_at, updated_at`,
		q.TenantID, derefString(q.APIKeyID), q.RPM, q.TPM, q.DailyRequests, q.DailyTokens, q.MonthlyCostUSD).
		Scan(&out.ID, &out.TenantID, &keyID, &out.RPM, &out.TPM, &out.DailyRequests, &out.DailyTokens, &out.MonthlyCostUSD, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return out, err
	}
	if keyID.Valid {
		out.APIKeyID = &keyID.String
	}
	return out, nil
}

// ListQuotas returns quotas, optionally filtered by tenant.
func (s *Store) ListQuotas(ctx context.Context, tenantID string) ([]Quota, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, tenant_id::text, api_key_id::text, rpm, tpm, daily_requests, daily_tokens, monthly_cost_usd::float8, created_at, updated_at
		FROM quotas WHERE ($1 = '' OR tenant_id::text = $1) ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Quota
	for rows.Next() {
		var q Quota
		var keyID sql.NullString
		if err := rows.Scan(&q.ID, &q.TenantID, &keyID, &q.RPM, &q.TPM, &q.DailyRequests, &q.DailyTokens, &q.MonthlyCostUSD, &q.CreatedAt, &q.UpdatedAt); err != nil {
			return nil, err
		}
		if keyID.Valid {
			q.APIKeyID = &keyID.String
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// DeleteQuota removes a quota by id.
func (s *Store) DeleteQuota(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM quotas WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpsertPrice sets the cost of a provider/model pair.
func (s *Store) UpsertPrice(ctx context.Context, p ModelPrice) (ModelPrice, error) {
	var out ModelPrice
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO model_prices(provider, model, input_usd_per_1m, output_usd_per_1m)
		VALUES($1,$2,$3,$4)
		ON CONFLICT (provider, model) DO UPDATE SET input_usd_per_1m=EXCLUDED.input_usd_per_1m,
			output_usd_per_1m=EXCLUDED.output_usd_per_1m, updated_at=now()
		RETURNING id::text, provider, model, input_usd_per_1m::float8, output_usd_per_1m::float8, created_at, updated_at`,
		p.Provider, p.Model, p.InputUSDPer1M, p.OutputUSDPer1M).
		Scan(&out.ID, &out.Provider, &out.Model, &out.InputUSDPer1M, &out.OutputUSDPer1M, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

// ListPrices returns all configured model prices.
func (s *Store) ListPrices(ctx context.Context) ([]ModelPrice, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, provider, model, input_usd_per_1m::float8, output_usd_per_1m::float8, created_at, updated_at
		FROM model_prices ORDER BY provider, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelPrice
	for rows.Next() {
		var p ModelPrice
		if err := rows.Scan(&p.ID, &p.Provider, &p.Model, &p.InputUSDPer1M, &p.OutputUSDPer1M, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePrice removes a price by id.
func (s *Store) DeletePrice(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM model_prices WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Usage returns aggregated daily usage for a date range.
func (s *Store) Usage(ctx context.Context, from, to time.Time, tenantID string) ([]UsageSummary, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT tenant_id::text, provider, model, day::text,
		       SUM(requests), SUM(prompt_tokens), SUM(completion_tokens), SUM(cost_usd)::float8
		FROM usage_daily
		WHERE day >= $1::date AND day <= $2::date AND ($3 = '' OR tenant_id::text = $3)
		GROUP BY tenant_id, provider, model, day
		ORDER BY day DESC, tenant_id, provider, model`, from, to, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSummary
	for rows.Next() {
		var u UsageSummary
		if err := rows.Scan(&u.TenantID, &u.Provider, &u.Model, &u.Day, &u.Requests, &u.PromptTokens, &u.CompletionTokens, &u.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListAudit returns audit records newest first.
func (s *Store) ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]AuditRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT request_id, tenant_id::text, key_prefix, model, provider, stream, status, cache,
		       prompt_tokens, completion_tokens, cost_usd::float8, latency_ms, error_code, pii_redacted, created_at
		FROM request_audit
		WHERE ($1 = '' OR tenant_id::text = $1)
		ORDER BY created_at DESC LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRecord
	for rows.Next() {
		var a AuditRecord
		if err := rows.Scan(&a.RequestID, &a.TenantID, &a.KeyPrefix, &a.Model, &a.Provider, &a.Stream, &a.Status, &a.Cache,
			&a.PromptTokens, &a.CompletionTokens, &a.CostUSD, &a.LatencyMS, &a.ErrorCode, &a.PIIRedacted, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PurgeExpired removes expired semantic cache entries and old audits.
func (s *Store) PurgeExpired(ctx context.Context, auditRetention time.Duration) error {
	if ok, err := s.HasVector(ctx); err == nil && ok {
		if _, err := s.Pool.Exec(ctx, `DELETE FROM semantic_cache WHERE expires_at < now()`); err != nil {
			return err
		}
	}
	cutoff := time.Now().Add(-auditRetention)
	_, err := s.Pool.Exec(ctx, `DELETE FROM request_audit WHERE created_at < $1`, cutoff)
	return err
}

// PurgeSemanticCache removes semantic cache entries by optional tenant/model.
func (s *Store) PurgeSemanticCache(ctx context.Context, tenantID, model string) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `
		DELETE FROM semantic_cache
		WHERE ($1 = '' OR tenant_id::text = $1) AND ($2 = '' OR model = $2)`, tenantID, model)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// TotalMonthlyCost returns the cost recorded for a tenant in a month.
func (s *Store) TotalMonthlyCost(ctx context.Context, tenantID string, monthStart time.Time) (float64, error) {
	var total float64
	err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost_usd),0)::float8 FROM usage_daily
		WHERE tenant_id=$1 AND day >= $2::date`, tenantID, monthStart).Scan(&total)
	return total, err
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func validKind(kind string) bool {
	switch kind {
	case "mock", "ollama", "openai_compatible":
		return true
	default:
		return false
	}
}

// ValidateProvider performs basic validation before writing.
func ValidateProvider(p Provider) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("provider name is required")
	}
	if !validKind(p.Kind) {
		return fmt.Errorf("unsupported provider kind %q", p.Kind)
	}
	if p.TimeoutMS <= 0 {
		p.TimeoutMS = 30000
	}
	if p.MaxRetries < 0 {
		return fmt.Errorf("max_retries must be >= 0")
	}
	if p.Kind != "mock" && strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("base_url is required for %s", p.Kind)
	}
	return nil
}
