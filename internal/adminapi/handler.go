// Package adminapi implements the isolated administrative API.
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/yugrinovD/gogate/internal/apierr"
	"github.com/yugrinovD/gogate/internal/cache"
	"github.com/yugrinovD/gogate/internal/config"
	"github.com/yugrinovD/gogate/internal/control"
	"github.com/yugrinovD/gogate/internal/orchestrator"
	"github.com/yugrinovD/gogate/internal/security"
	"github.com/yugrinovD/gogate/internal/store"
	"github.com/yugrinovD/gogate/internal/version"
)

// Handler serves /admin/v1.
type Handler struct {
	cfg      config.Config
	store    *store.Store
	control  *control.Manager
	orch     *orchestrator.Orchestrator
	redis    *redis.Client
	exact    *cache.Exact
	semantic *cache.Semantic
	log      *slog.Logger
}

// Options configures the admin API.
type Options struct {
	Config   config.Config
	Store    *store.Store
	Control  *control.Manager
	Orch     *orchestrator.Orchestrator
	Redis    *redis.Client
	Exact    *cache.Exact
	Semantic *cache.Semantic
	Logger   *slog.Logger
}

// New creates an admin API handler.
func New(o Options) *Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Handler{
		cfg: o.Config, store: o.Store, control: o.Control, orch: o.Orch,
		redis: o.Redis, exact: o.Exact, semantic: o.Semantic, log: o.Logger,
	}
}

// Register mounts admin routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/v1/health", h.health)
	mux.HandleFunc("GET /admin/v1/config", h.configInfo)
	mux.HandleFunc("POST /admin/v1/reload", h.reload)

	mux.HandleFunc("GET /admin/v1/tenants", h.listTenants)
	mux.HandleFunc("POST /admin/v1/tenants", h.createTenant)
	mux.HandleFunc("DELETE /admin/v1/tenants/{id}", h.deleteTenant)

	mux.HandleFunc("GET /admin/v1/keys", h.listKeys)
	mux.HandleFunc("POST /admin/v1/keys", h.createKey)
	mux.HandleFunc("PATCH /admin/v1/keys/{id}", h.updateKey)
	mux.HandleFunc("DELETE /admin/v1/keys/{id}", h.deleteKey)

	mux.HandleFunc("GET /admin/v1/providers", h.listProviders)
	mux.HandleFunc("POST /admin/v1/providers", h.createProvider)
	mux.HandleFunc("PATCH /admin/v1/providers/{id}", h.updateProvider)
	mux.HandleFunc("DELETE /admin/v1/providers/{id}", h.deleteProvider)
	mux.HandleFunc("POST /admin/v1/providers/{id}/test", h.testProvider)

	mux.HandleFunc("GET /admin/v1/quotas", h.listQuotas)
	mux.HandleFunc("POST /admin/v1/quotas", h.upsertQuota)
	mux.HandleFunc("DELETE /admin/v1/quotas/{id}", h.deleteQuota)

	mux.HandleFunc("GET /admin/v1/prices", h.listPrices)
	mux.HandleFunc("POST /admin/v1/prices", h.upsertPrice)
	mux.HandleFunc("DELETE /admin/v1/prices/{id}", h.deletePrice)

	mux.HandleFunc("GET /admin/v1/usage", h.usage)
	mux.HandleFunc("GET /admin/v1/audit", h.audit)
	mux.HandleFunc("POST /admin/v1/cache/purge", h.purgeCache)
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	dbStatus := componentPing(ctx, h.store.Ping)
	redisStatus := h.pingRedis(ctx)
	code := http.StatusOK
	if !boolField(dbStatus, "ok") || !boolField(redisStatus, "ok") {
		code = http.StatusServiceUnavailable
	}
	status := map[string]any{
		"version":   version.String(),
		"database":  dbStatus,
		"redis":     redisStatus,
		"providers": []any{},
		"breakers":  h.orch.Breakers().Snapshot(),
	}
	if snap := h.control.Snapshot(); snap != nil {
		providers := make([]map[string]any, 0)
		for _, p := range snap.Providers.Providers() {
			providers = append(providers, map[string]any{
				"name": p.Name(), "kind": p.Kind(), "models": p.Models(),
				"breaker": h.orch.Breakers().State(p.Name()),
			})
		}
		status["providers"] = providers
		status["problems"] = snap.Providers.Problems()
	}
	writeJSON(w, code, status)
}

func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func (h *Handler) pingRedis(ctx context.Context) map[string]any {
	if h.redis == nil {
		return map[string]any{"ok": false, "error": "redis is not configured"}
	}
	return componentPing(ctx, func(context.Context) error { return h.redis.Ping(ctx).Err() })
}

func componentPing(ctx context.Context, fn func(context.Context) error) map[string]any {
	if err := fn(ctx); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func (h *Handler) configInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.cfg.Redacted())
}

func (h *Handler) reload(w http.ResponseWriter, r *http.Request) {
	if err := h.control.Reload(r.Context()); err != nil {
		apierr.Internal(w, r, "reload failed")
		return
	}
	snap := h.control.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tenants": len(snap.Tenants), "keys": len(snap.KeysByPrefix),
		"providers": len(snap.Providers.Providers()), "models": len(snap.Providers.Models()),
	})
}

func (h *Handler) reloadQuiet(ctx context.Context) {
	if err := h.control.Reload(ctx); err != nil {
		h.log.Error("control plane reload failed", "error", err)
		return
	}
	if err := control.PublishReload(ctx, h.redis); err != nil {
		h.log.Warn("control plane reload notification failed", "error", err)
	}
}

func (h *Handler) listTenants(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListTenants(r.Context())
	if err != nil {
		apierr.Internal(w, r, "list tenants failed")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		apierr.BadRequest(w, r, "invalid_name", "name is required")
		return
	}
	t, err := h.store.CreateTenant(r.Context(), in.Name)
	if err != nil {
		apierr.Internal(w, r, "create tenant failed")
		return
	}
	h.reloadQuiet(r.Context())
	writeJSON(w, http.StatusCreated, t)
}

func (h *Handler) deleteTenant(w http.ResponseWriter, r *http.Request) {
	if !validateID(w, r, r.PathValue("id")) {
		return
	}
	if err := h.store.DeleteTenant(r.Context(), r.PathValue("id")); err != nil {
		notFoundOr500(w, r, err, "delete tenant failed")
		return
	}
	h.reloadQuiet(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListAPIKeys(r.Context(), r.URL.Query().Get("tenant_id"))
	if err != nil {
		apierr.Internal(w, r, "list keys failed")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TenantID  string     `json:"tenant_id"`
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.TenantID == "" || in.Name == "" {
		apierr.BadRequest(w, r, "invalid_key", "tenant_id and name are required")
		return
	}
	if !validateID(w, r, in.TenantID) {
		return
	}
	if _, err := h.store.GetTenant(r.Context(), in.TenantID); err != nil {
		notFoundOr500(w, r, err, "tenant not found")
		return
	}
	mat, err := security.GenerateKey(h.cfg.Env, h.cfg.Pepper)
	if err != nil {
		apierr.Internal(w, r, "generate key failed")
		return
	}
	key, err := h.store.CreateAPIKey(r.Context(), store.APIKey{
		TenantID: in.TenantID, Name: in.Name, Prefix: mat.Prefix, Hash: mat.Hash, Enabled: true, ExpiresAt: in.ExpiresAt,
	})
	if err != nil {
		apierr.Internal(w, r, "create key failed")
		return
	}
	h.reloadQuiet(r.Context())
	writeJSON(w, http.StatusCreated, map[string]any{
		"key": mat.Plaintext, "id": key.ID, "tenant_id": key.TenantID,
		"name": key.Name, "key_prefix": key.Prefix, "enabled": key.Enabled,
		"created_at": key.CreatedAt, "expires_at": key.ExpiresAt,
		"warning": "store this key now; it is not shown again",
	})
}

func (h *Handler) updateKey(w http.ResponseWriter, r *http.Request) {
	if !validateID(w, r, r.PathValue("id")) {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		apierr.BadRequest(w, r, "invalid_update", "enabled is required")
		return
	}
	if err := h.store.SetAPIKeyEnabled(r.Context(), r.PathValue("id"), *in.Enabled); err != nil {
		notFoundOr500(w, r, err, "update key failed")
		return
	}
	h.reloadQuiet(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	if !validateID(w, r, r.PathValue("id")) {
		return
	}
	if err := h.store.DeleteAPIKey(r.Context(), r.PathValue("id")); err != nil {
		notFoundOr500(w, r, err, "delete key failed")
		return
	}
	h.reloadQuiet(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListProviders(r.Context())
	if err != nil {
		apierr.Internal(w, r, "list providers failed")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

type providerInput struct {
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	BaseURL    string          `json:"base_url"`
	APIKeyEnv  string          `json:"api_key_env"`
	Models     []string        `json:"models"`
	Priority   int             `json:"priority"`
	Enabled    *bool           `json:"enabled"`
	TimeoutMS  int             `json:"timeout_ms"`
	MaxRetries int             `json:"max_retries"`
	Config     json.RawMessage `json:"config"`
}

func (in providerInput) toStore() store.Provider {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	cfg := in.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	timeout := in.TimeoutMS
	if timeout <= 0 {
		timeout = 30000
	}
	return store.Provider{
		Name: in.Name, Kind: in.Kind, BaseURL: in.BaseURL, APIKeyEnv: in.APIKeyEnv,
		Models: in.Models, Priority: in.Priority, Enabled: enabled,
		TimeoutMS: timeout, MaxRetries: in.MaxRetries, Config: cfg,
	}
}

type providerPatch struct {
	Name       *string          `json:"name"`
	Kind       *string          `json:"kind"`
	BaseURL    *string          `json:"base_url"`
	APIKeyEnv  *string          `json:"api_key_env"`
	Models     *[]string        `json:"models"`
	Priority   *int             `json:"priority"`
	Enabled    *bool            `json:"enabled"`
	TimeoutMS  *int             `json:"timeout_ms"`
	MaxRetries *int             `json:"max_retries"`
	Config     *json.RawMessage `json:"config"`
}

func (in providerPatch) apply(existing store.Provider) (store.Provider, error) {
	p := existing
	if in.Name != nil && *in.Name != existing.Name {
		return p, fmt.Errorf("provider name is immutable; create a new provider instead")
	}
	if in.Kind != nil {
		p.Kind = *in.Kind
	}
	if in.BaseURL != nil {
		p.BaseURL = *in.BaseURL
	}
	if in.APIKeyEnv != nil {
		p.APIKeyEnv = *in.APIKeyEnv
	}
	if in.Models != nil {
		p.Models = append([]string(nil), (*in.Models)...)
	}
	if in.Priority != nil {
		p.Priority = *in.Priority
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if in.TimeoutMS != nil {
		if *in.TimeoutMS <= 0 {
			return p, fmt.Errorf("timeout_ms must be positive")
		}
		p.TimeoutMS = *in.TimeoutMS
	}
	if in.MaxRetries != nil {
		p.MaxRetries = *in.MaxRetries
	}
	if in.Config != nil {
		p.Config = *in.Config
	}
	if len(p.Config) == 0 {
		p.Config = json.RawMessage(`{}`)
	}
	if p.TimeoutMS <= 0 {
		p.TimeoutMS = 30000
	}
	return p, nil
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var in providerInput
	if !decode(w, r, &in) {
		return
	}
	p := in.toStore()
	if err := store.ValidateProvider(p); err != nil {
		apierr.BadRequest(w, r, "invalid_provider", err.Error())
		return
	}
	if err := security.ValidateEnvName(p.APIKeyEnv, h.cfg.UpstreamKeyEnvPrefix); err != nil {
		apierr.BadRequest(w, r, "invalid_api_key_env", err.Error())
		return
	}
	if p.Kind != "mock" {
		if err := security.ValidateUpstreamURL(p.BaseURL, h.cfg.AllowPrivateUpstream); err != nil {
			apierr.BadRequest(w, r, "invalid_provider_url", err.Error())
			return
		}
	}
	out, err := h.store.CreateProvider(r.Context(), p)
	if err != nil {
		apierr.Internal(w, r, "create provider failed")
		return
	}
	h.reloadQuiet(r.Context())
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validateID(w, r, id) {
		return
	}
	var in providerPatch
	if !decode(w, r, &in) {
		return
	}
	existing, err := h.store.GetProvider(r.Context(), id)
	if err != nil {
		notFoundOr500(w, r, err, "provider not found")
		return
	}
	p, err := in.apply(existing)
	if err != nil {
		apierr.BadRequest(w, r, "invalid_provider", err.Error())
		return
	}
	if err := store.ValidateProvider(p); err != nil {
		apierr.BadRequest(w, r, "invalid_provider", err.Error())
		return
	}
	if err := security.ValidateEnvName(p.APIKeyEnv, h.cfg.UpstreamKeyEnvPrefix); err != nil {
		apierr.BadRequest(w, r, "invalid_api_key_env", err.Error())
		return
	}
	if p.Kind != "mock" {
		if err := security.ValidateUpstreamURL(p.BaseURL, h.cfg.AllowPrivateUpstream); err != nil {
			apierr.BadRequest(w, r, "invalid_provider_url", err.Error())
			return
		}
	}
	out, err := h.store.UpdateProvider(r.Context(), id, p)
	if err != nil {
		notFoundOr500(w, r, err, "update provider failed")
		return
	}
	h.reloadQuiet(r.Context())
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if !validateID(w, r, r.PathValue("id")) {
		return
	}
	if err := h.store.DeleteProvider(r.Context(), r.PathValue("id")); err != nil {
		notFoundOr500(w, r, err, "delete provider failed")
		return
	}
	h.reloadQuiet(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validateID(w, r, id) {
		return
	}
	p, err := h.store.GetProvider(r.Context(), id)
	if err != nil {
		notFoundOr500(w, r, err, "provider not found")
		return
	}
	snap := h.control.Snapshot()
	prov, ok := snap.Providers.ByName(p.Name)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "provider": p.Name, "error": "provider is not active"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := prov.Ping(ctx); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "provider": p.Name, "latency_ms": time.Since(start).Milliseconds(), "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": p.Name, "latency_ms": time.Since(start).Milliseconds()})
}

func (h *Handler) listQuotas(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListQuotas(r.Context(), r.URL.Query().Get("tenant_id"))
	if err != nil {
		apierr.Internal(w, r, "list quotas failed")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) upsertQuota(w http.ResponseWriter, r *http.Request) {
	var in store.Quota
	if !decode(w, r, &in) {
		return
	}
	if in.TenantID == "" {
		apierr.BadRequest(w, r, "invalid_quota", "tenant_id is required")
		return
	}
	if !validateID(w, r, in.TenantID) {
		return
	}
	if _, err := h.store.GetTenant(r.Context(), in.TenantID); err != nil {
		notFoundOr500(w, r, err, "tenant not found")
		return
	}
	if in.APIKeyID != nil {
		if !validateID(w, r, *in.APIKeyID) {
			return
		}
		key, err := h.store.GetAPIKeyByID(r.Context(), *in.APIKeyID)
		if err != nil {
			notFoundOr500(w, r, err, "api key not found")
			return
		}
		if key.TenantID != in.TenantID {
			apierr.BadRequest(w, r, "invalid_quota", "api_key_id belongs to another tenant")
			return
		}
	}
	if err := validateQuota(in); err != nil {
		apierr.BadRequest(w, r, "invalid_quota", err.Error())
		return
	}
	out, err := h.store.UpsertQuota(r.Context(), in)
	if err != nil {
		apierr.Internal(w, r, "upsert quota failed")
		return
	}
	h.reloadQuiet(r.Context())
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) deleteQuota(w http.ResponseWriter, r *http.Request) {
	if !validateID(w, r, r.PathValue("id")) {
		return
	}
	if err := h.store.DeleteQuota(r.Context(), r.PathValue("id")); err != nil {
		notFoundOr500(w, r, err, "delete quota failed")
		return
	}
	h.reloadQuiet(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listPrices(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListPrices(r.Context())
	if err != nil {
		apierr.Internal(w, r, "list prices failed")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) upsertPrice(w http.ResponseWriter, r *http.Request) {
	var in store.ModelPrice
	if !decode(w, r, &in) {
		return
	}
	if in.Provider == "" || in.Model == "" {
		apierr.BadRequest(w, r, "invalid_price", "provider and model are required")
		return
	}
	if in.InputUSDPer1M < 0 || in.OutputUSDPer1M < 0 {
		apierr.BadRequest(w, r, "invalid_price", "prices must be non-negative")
		return
	}
	out, err := h.store.UpsertPrice(r.Context(), in)
	if err != nil {
		apierr.Internal(w, r, "upsert price failed")
		return
	}
	h.reloadQuiet(r.Context())
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) deletePrice(w http.ResponseWriter, r *http.Request) {
	if !validateID(w, r, r.PathValue("id")) {
		return
	}
	if err := h.store.DeletePrice(r.Context(), r.PathValue("id")); err != nil {
		notFoundOr500(w, r, err, "delete price failed")
		return
	}
	h.reloadQuiet(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	to, err := parseDate(r.URL.Query().Get("to"), time.Now().UTC())
	if err != nil {
		apierr.BadRequest(w, r, "invalid_date", "to must be YYYY-MM-DD")
		return
	}
	from, err := parseDate(r.URL.Query().Get("from"), to.AddDate(0, 0, -7))
	if err != nil {
		apierr.BadRequest(w, r, "invalid_date", "from must be YYYY-MM-DD")
		return
	}
	items, err := h.store.Usage(r.Context(), from, to, r.URL.Query().Get("tenant_id"))
	if err != nil {
		apierr.Internal(w, r, "usage query failed")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) audit(w http.ResponseWriter, r *http.Request) {
	limit, err := intQuery(r, "limit", 100, 1, 500)
	if err != nil {
		apierr.BadRequest(w, r, "invalid_limit", err.Error())
		return
	}
	offset, err := intQuery(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		apierr.BadRequest(w, r, "invalid_offset", err.Error())
		return
	}
	items, err := h.store.ListAudit(r.Context(), r.URL.Query().Get("tenant_id"), limit, offset)
	if err != nil {
		apierr.Internal(w, r, "audit query failed")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) purgeCache(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TenantID string `json:"tenant_id"`
		Model    string `json:"model"`
		Semantic *bool  `json:"semantic"`
		Exact    bool   `json:"exact"`
	}
	if !decode(w, r, &in) {
		return
	}
	semanticRequested := in.Semantic != nil && *in.Semantic
	if !semanticRequested && !in.Exact {
		apierr.BadRequest(w, r, "invalid_cache_purge", "set semantic=true and/or exact=true")
		return
	}
	if in.TenantID != "" && !validateID(w, r, in.TenantID) {
		return
	}
	if semanticRequested && h.semantic == nil {
		apierr.BadRequest(w, r, "semantic_cache_disabled", "semantic cache is not enabled")
		return
	}
	var semantic, exact int64
	var err error
	if semanticRequested {
		semantic, err = h.store.PurgeSemanticCache(r.Context(), in.TenantID, in.Model)
		if err != nil {
			apierr.Internal(w, r, "purge semantic cache failed")
			return
		}
	}
	if in.Exact && h.exact != nil {
		exact, err = h.exact.Flush(r.Context())
		if err != nil {
			apierr.Internal(w, r, "purge exact cache failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"semantic_deleted": semantic,
		"exact_deleted":    exact,
		"exact_scope":      "global",
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apierr.Write(w, r, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", "request body is too large")
			return false
		}
		apierr.BadRequest(w, r, "invalid_json", "invalid JSON body")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		apierr.BadRequest(w, r, "invalid_json", "invalid JSON body")
		return false
	}
	return true
}

func parseDate(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

func intQuery(r *http.Request, key string, def, min, max int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, min, max)
	}
	return n, nil
}

func validateID(w http.ResponseWriter, r *http.Request, id string) bool {
	if _, err := uuid.Parse(id); err != nil {
		apierr.BadRequest(w, r, "invalid_id", "invalid UUID")
		return false
	}
	return true
}

func validateQuota(q store.Quota) error {
	negative := func(name string, v *int) error {
		if v != nil && *v < 0 {
			return fmt.Errorf("%s must be non-negative", name)
		}
		return nil
	}
	if err := negative("rpm", q.RPM); err != nil {
		return err
	}
	if err := negative("tpm", q.TPM); err != nil {
		return err
	}
	if q.DailyRequests != nil && *q.DailyRequests < 0 {
		return fmt.Errorf("daily_requests must be non-negative")
	}
	if q.DailyTokens != nil && *q.DailyTokens < 0 {
		return fmt.Errorf("daily_tokens must be non-negative")
	}
	if q.MonthlyCostUSD != nil && *q.MonthlyCostUSD < 0 {
		return fmt.Errorf("monthly_cost_usd must be non-negative")
	}
	return nil
}

func notFoundOr500(w http.ResponseWriter, r *http.Request, err error, msg string) {
	if store.IsNotFound(err) {
		apierr.NotFound(w, r, "not_found", "resource not found")
		return
	}
	apierr.Internal(w, r, msg)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
