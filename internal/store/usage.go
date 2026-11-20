package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// InsertUsage writes a batch of daily aggregates atomically.
func (s *Store) InsertUsage(ctx context.Context, records []UsageRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin usage batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, r := range records {
		batch.Queue(`
			INSERT INTO usage_daily(tenant_id, api_key_id, provider, model, day, requests, prompt_tokens, completion_tokens, cost_usd)
			VALUES($1,$2,$3,$4,$5::date,$6,$7,$8,$9)
			ON CONFLICT (tenant_id, api_key_id, provider, model, day) DO UPDATE SET
				requests = usage_daily.requests + EXCLUDED.requests,
				prompt_tokens = usage_daily.prompt_tokens + EXCLUDED.prompt_tokens,
				completion_tokens = usage_daily.completion_tokens + EXCLUDED.completion_tokens,
				cost_usd = usage_daily.cost_usd + EXCLUDED.cost_usd,
				updated_at = now()`,
			r.TenantID, r.APIKeyID, r.Provider, r.Model, r.Day, r.Requests, r.PromptTokens, r.CompletionTokens, r.CostUSD)
	}
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < len(records); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("insert usage record %d: %w", i, err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("close usage batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit usage batch: %w", err)
	}
	return nil
}

// InsertAudit writes a batch of request audit records.
func (s *Store) InsertAudit(ctx context.Context, records []AuditRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, r := range records {
		batch.Queue(`
			INSERT INTO request_audit(request_id, tenant_id, key_prefix, model, provider, stream, status, cache,
				prompt_tokens, completion_tokens, cost_usd, latency_ms, error_code, pii_redacted)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			r.RequestID, r.TenantID, r.KeyPrefix, r.Model, r.Provider, r.Stream, r.Status, r.Cache,
			r.PromptTokens, r.CompletionTokens, r.CostUSD, r.LatencyMS, r.ErrorCode, r.PIIRedacted)
	}
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < len(records); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("insert audit record %d: %w", i, err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("close audit batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit batch: %w", err)
	}
	return nil
}
