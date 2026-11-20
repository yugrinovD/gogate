// Package control maintains an atomically swappable in-memory snapshot of the
// PostgreSQL control plane. Hot-path requests never read PostgreSQL directly.
package control

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yugrinovD/gogate/internal/provider"
	"github.com/yugrinovD/gogate/internal/provider/registry"
	"github.com/yugrinovD/gogate/internal/security"
	"github.com/yugrinovD/gogate/internal/store"
)

// Key is an immutable API key entry.
type Key struct {
	ID        string
	TenantID  string
	Name      string
	Prefix    string
	Hash      []byte
	Enabled   bool
	ExpiresAt *time.Time
}

// Tenant is an immutable tenant entry.
type Tenant struct {
	ID   string
	Name string
}

// Quota is an effective quota for a tenant or key.
type Quota struct {
	RPM            *int
	TPM            *int
	DailyRequests  *int64
	DailyTokens    *int64
	MonthlyCostUSD *float64
}

// Price holds per-token prices in USD.
type Price struct {
	Provider       string
	Model          string
	InputUSDPer1M  float64
	OutputUSDPer1M float64
}

// Snapshot is an immutable view of the control plane.
type Snapshot struct {
	KeysByPrefix map[string]Key
	Tenants      map[string]Tenant
	TenantQuotas map[string]Quota
	KeyQuotas    map[string]Quota
	Prices       map[string]Price
	Providers    *registry.Registry
	LoadedAt     time.Time
}

// PriceFor returns the configured price and whether a price row exists.
func (s *Snapshot) PriceFor(providerName, model string) (Price, bool) {
	if p, ok := s.Prices[providerName+"\x00"+model]; ok {
		return p, true
	}
	return Price{Provider: providerName, Model: model}, false
}

// Manager reloads the control plane snapshot.
type Manager struct {
	store          *store.Store
	log            *slog.Logger
	allowPrivate   bool
	upstreamPrefix string
	httpClient     *http.Client
	snap           atomic.Pointer[Snapshot]
	reloadMu       sync.Mutex
}

// NewManager creates a manager.
func NewManager(st *store.Store, log *slog.Logger, allowPrivate bool, httpClient *http.Client, upstreamPrefix string) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if upstreamPrefix == "" {
		upstreamPrefix = "GOGATE_UPSTREAM_"
	}
	m := &Manager{store: st, log: log, allowPrivate: allowPrivate, upstreamPrefix: upstreamPrefix, httpClient: httpClient}
	empty := &Snapshot{
		KeysByPrefix: map[string]Key{},
		Tenants:      map[string]Tenant{},
		TenantQuotas: map[string]Quota{},
		KeyQuotas:    map[string]Quota{},
		Prices:       map[string]Price{},
		Providers:    registry.NewRegistry(nil, registry.Options{Logger: log, AllowPrivate: allowPrivate}),
		LoadedAt:     time.Now(),
	}
	m.snap.Store(empty)
	return m
}

// Snapshot returns the current immutable snapshot.
func (m *Manager) Snapshot() *Snapshot { return m.snap.Load() }

// Reload reads all control-plane rows and atomically swaps the snapshot.
func (m *Manager) Reload(ctx context.Context) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	start := time.Now()
	tenants, err := m.store.ListTenants(ctx)
	if err != nil {
		return err
	}
	keys, err := m.store.ListAPIKeys(ctx, "")
	if err != nil {
		return err
	}
	providers, err := m.store.ListProviders(ctx)
	if err != nil {
		return err
	}
	quotas, err := m.store.ListQuotas(ctx, "")
	if err != nil {
		return err
	}
	prices, err := m.store.ListPrices(ctx)
	if err != nil {
		return err
	}

	snap := &Snapshot{
		KeysByPrefix: make(map[string]Key, len(keys)),
		Tenants:      make(map[string]Tenant, len(tenants)),
		TenantQuotas: make(map[string]Quota),
		KeyQuotas:    make(map[string]Quota),
		Prices:       make(map[string]Price, len(prices)),
		LoadedAt:     time.Now(),
	}
	for _, t := range tenants {
		snap.Tenants[t.ID] = Tenant{ID: t.ID, Name: t.Name}
	}
	for _, k := range keys {
		snap.KeysByPrefix[k.Prefix] = Key{
			ID: k.ID, TenantID: k.TenantID, Name: k.Name, Prefix: k.Prefix,
			Hash: k.Hash, Enabled: k.Enabled, ExpiresAt: k.ExpiresAt,
		}
	}
	for _, q := range quotas {
		qq := Quota{RPM: q.RPM, TPM: q.TPM, DailyRequests: q.DailyRequests, DailyTokens: q.DailyTokens, MonthlyCostUSD: q.MonthlyCostUSD}
		if q.APIKeyID == nil {
			snap.TenantQuotas[q.TenantID] = qq
		} else {
			snap.KeyQuotas[*q.APIKeyID] = qq
		}
	}
	for _, p := range prices {
		snap.Prices[p.Provider+"\x00"+p.Model] = Price{
			Provider: p.Provider, Model: p.Model,
			InputUSDPer1M: p.InputUSDPer1M, OutputUSDPer1M: p.OutputUSDPer1M,
		}
	}
	configs := make([]provider.Config, 0, len(providers))
	for _, p := range providers {
		if !p.Enabled {
			continue
		}
		apiKey := ""
		if p.APIKeyEnv != "" {
			if err := security.ValidateEnvName(p.APIKeyEnv, m.upstreamPrefix); err != nil {
				m.log.Warn("provider skipped: unsafe api_key_env", "provider", p.Name, "error", err)
				continue
			}
			apiKey = os.Getenv(p.APIKeyEnv)
		}
		configs = append(configs, provider.Config{
			Name:       p.Name,
			Kind:       p.Kind,
			BaseURL:    p.BaseURL,
			APIKeyEnv:  p.APIKeyEnv,
			APIKey:     apiKey,
			Models:     append([]string(nil), p.Models...),
			Priority:   p.Priority,
			Timeout:    time.Duration(p.TimeoutMS) * time.Millisecond,
			MaxRetries: p.MaxRetries,
			RawConfig:  append([]byte(nil), p.Config...),
		})
	}
	sort.Slice(configs, func(i, j int) bool {
		if configs[i].Priority == configs[j].Priority {
			return configs[i].Name < configs[j].Name
		}
		return configs[i].Priority < configs[j].Priority
	})
	snap.Providers = registry.NewRegistry(configs, registry.Options{
		Logger:       m.log,
		HTTPClient:   m.httpClient,
		AllowPrivate: m.allowPrivate,
	})
	m.snap.Store(snap)
	m.log.Info("control plane reloaded",
		"tenants", len(snap.Tenants),
		"keys", len(snap.KeysByPrefix),
		"providers", len(snap.Providers.Providers()),
		"models", len(snap.Providers.Models()),
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}
