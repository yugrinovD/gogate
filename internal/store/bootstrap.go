package store

import (
	"context"
)

// BootstrapProvider describes a provider seeded from environment on startup.
type BootstrapProvider struct {
	Name       string
	Kind       string
	BaseURL    string
	APIKeyEnv  string
	Models     []string
	Priority   int
	TimeoutMS  int
	MaxRetries int
}

// BootstrapProviders inserts default providers without overwriting existing
// admin-managed rows.
func (s *Store) BootstrapProviders(ctx context.Context, providers []BootstrapProvider) error {
	var existing int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM providers`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	for _, p := range providers {
		if len(p.Models) == 0 {
			continue
		}
		timeout := p.TimeoutMS
		if timeout <= 0 {
			timeout = 30000
		}
		retries := p.MaxRetries
		if retries <= 0 {
			retries = 1
		}
		if _, err := s.Pool.Exec(ctx, `
			INSERT INTO providers(name, kind, base_url, api_key_env, models, priority, enabled, timeout_ms, max_retries, config)
			VALUES($1,$2,$3,$4,$5,$6,true,$7,$8,'{}'::jsonb)
			ON CONFLICT (name) DO NOTHING`,
			p.Name, p.Kind, p.BaseURL, p.APIKeyEnv, p.Models, p.Priority, timeout, retries); err != nil {
			return err
		}
	}
	return nil
}
