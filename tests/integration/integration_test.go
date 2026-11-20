//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/yugrinovD/gogate/internal/adminapi"
	"github.com/yugrinovD/gogate/internal/cache"
	"github.com/yugrinovD/gogate/internal/config"
	"github.com/yugrinovD/gogate/internal/control"
	"github.com/yugrinovD/gogate/internal/health"
	"github.com/yugrinovD/gogate/internal/httpapi"
	"github.com/yugrinovD/gogate/internal/logging"
	"github.com/yugrinovD/gogate/internal/middleware"
	"github.com/yugrinovD/gogate/internal/observability"
	"github.com/yugrinovD/gogate/internal/orchestrator"
	"github.com/yugrinovD/gogate/internal/provider"
	"github.com/yugrinovD/gogate/internal/ratelimit"
	"github.com/yugrinovD/gogate/internal/rediscli"
	"github.com/yugrinovD/gogate/internal/reqctx"
	"github.com/yugrinovD/gogate/internal/security"
	"github.com/yugrinovD/gogate/internal/store"
	"github.com/yugrinovD/gogate/internal/usage"
)

type app struct {
	cfg      config.Config
	store    *store.Store
	control  *control.Manager
	orch     *orchestrator.Orchestrator
	writer   *usage.Writer
	public   *httptest.Server
	admin    *httptest.Server
	tenantID string
	apiKey   string
}

func TestGatewayEndToEnd(t *testing.T) {
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "pgvector/pgvector:pg17",
		postgres.WithDatabase("gogate"),
		postgres.WithUsername("gogate"),
		postgres.WithPassword("gogate"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	conn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}

	redisContainer, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { _ = redisContainer.Terminate(context.Background()) })
	redisEndpoint, err := redisContainer.Endpoint(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(ctx, conn, 10, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureSemanticSchema(ctx); err != nil {
		t.Fatal(err)
	}

	rdb, err := rediscli.New(ctx, rediscli.Options{Addr: redisEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	cfg := config.Config{
		Env: "test", Pepper: "0123456789abcdef", AdminToken: "admin-token",
		MaxBodyBytes: 1 << 20, MaxOutputTokens: 128, MaxMessages: 16,
		RequestTimeout: 30 * time.Second, FirstByteTimeout: 5 * time.Second,
		StreamIdleTimeout: 30 * time.Second, CacheEnabled: true, CacheTTL: time.Minute,
		RateLimitEnabled: true, AuditEnabled: true, PIIRedactionEnabled: true, BudgetEnabled: true,
		AllowPrivateUpstream: true,
	}
	if err := st.BootstrapProviders(ctx, []store.BootstrapProvider{
		{Name: "mock", Kind: "mock", Models: []string{"mock", "mock-fast"}, Priority: 100, MaxRetries: 1},
	}); err != nil {
		t.Fatal(err)
	}
	badCfg, _ := json.Marshal(map[string]any{"fail_before_first": true})
	if _, err := st.CreateProvider(ctx, store.Provider{
		Name: "fallback-bad", Kind: "mock", Models: []string{"fallback"}, Priority: 1, Enabled: true,
		TimeoutMS: 5000, MaxRetries: 0, Config: badCfg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProvider(ctx, store.Provider{
		Name: "fallback-good", Kind: "mock", Models: []string{"fallback"}, Priority: 2, Enabled: true,
		TimeoutMS: 5000, MaxRetries: 0, Config: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"mock", "mock"}, {"mock", "mock-fast"},
		{"fallback-bad", "fallback"}, {"fallback-good", "fallback"},
	} {
		if err := st.EnsureZeroPrice(ctx, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}

	httpClient := provider.NewHTTPClient()
	manager := control.NewManager(st, logger, true, httpClient, "GOGATE_UPSTREAM_")
	if err := manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewMetrics()
	writer := usage.NewWriter(st, logger, usage.Options{QueueSize: 1000, BatchSize: 10, FlushInterval: 50 * time.Millisecond})
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = writer.Close(c)
	})
	orch := orchestrator.New(manager, orchestrator.Options{FirstByteTimeout: 5 * time.Second, Logger: logger})
	limiter := ratelimit.New(rdb)
	exact := cache.NewExact(rdb, time.Minute)
	semantic := cache.NewSemantic(st, cache.NewMockEmbedder(""), 0.8, time.Minute)

	chat := httpapi.New(httpapi.Options{
		Config: cfg, Control: manager, Orch: orch, Limiter: limiter,
		Exact: exact, Semantic: semantic, Usage: writer, Metrics: metrics, Logger: logger,
	})
	healthHandler := health.New(st, rdb, true)
	adminHandler := adminapi.New(adminapi.Options{
		Config: cfg, Store: st, Control: manager, Orch: orch, Redis: rdb,
		Exact: exact, Semantic: semantic, Logger: logger,
	})

	snapshotFn := func() *control.Snapshot { return manager.Snapshot() }
	chatAuth := middleware.APIKey(snapshotFn, cfg.Pepper, cfg.MaxAPIKeyBytes, func(id reqctx.Identity) { writer.TouchKey(id.KeyID) })

	publicMux := http.NewServeMux()
	publicMux.HandleFunc("GET /healthz", healthHandler.Live)
	publicMux.HandleFunc("GET /readyz", healthHandler.Ready)
	publicMux.Handle("POST /v1/chat/completions", chatAuth(http.HandlerFunc(chat.ChatCompletions)))
	publicMux.Handle("GET /v1/models", chatAuth(http.HandlerFunc(chat.Models)))
	public := httptest.NewServer(middleware.RequestID(logging.New("error"))(middleware.BodyLimit(cfg.MaxBodyBytes)(publicMux)))
	t.Cleanup(public.Close)

	adminMux := http.NewServeMux()
	adminHandler.Register(adminMux)
	admin := httptest.NewServer(middleware.AdminToken(cfg.AdminToken)(adminMux))
	t.Cleanup(admin.Close)

	a := &app{cfg: cfg, store: st, control: manager, orch: orch, writer: writer, public: public, admin: admin}
	a.tenantID = a.createTenant(t, "demo")
	a.apiKey = a.createKey(t, a.tenantID, "demo", nil)
	a.upsertQuota(t, map[string]any{
		"tenant_id": a.tenantID, "rpm": 100000, "tpm": 100000000,
		"daily_requests": 10000000, "daily_tokens": 10000000000, "monthly_cost_usd": 1000,
	})

	t.Run("redis lua limiter", func(t *testing.T) {
		rpm := 1
		limiter := ratelimit.New(rdb)
		scope := ratelimit.Limit{ID: "{t:lua-test}:tenant", RPM: &rpm}
		first, err := limiter.Admit(ctx, []ratelimit.Limit{scope}, 1, 0)
		if err != nil || !first.Allowed {
			t.Fatalf("first decision=%+v err=%v", first, err)
		}
		second, err := limiter.Admit(ctx, []ratelimit.Limit{scope}, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if second.Allowed || second.Reason != "rpm" || second.RetryAfter <= 0 {
			t.Fatalf("second decision=%+v", second)
		}
	})

	t.Run("control plane reload", func(t *testing.T) {
		before := len(manager.Snapshot().KeysByPrefix)
		if err := manager.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		after := len(manager.Snapshot().KeysByPrefix)
		if after < before {
			t.Fatalf("keys disappeared after reload: %d -> %d", before, after)
		}
	})

	t.Run("models", func(t *testing.T) {
		status, body := a.request(t, http.MethodGet, "/v1/models", nil, a.apiKey)
		if status != 200 {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(string(body), "mock") || !strings.Contains(string(body), "fallback") {
			t.Fatalf("models=%s", body)
		}
	})

	t.Run("non-stream and cache", func(t *testing.T) {
		payload := map[string]any{
			"model":      "mock",
			"messages":   []map[string]any{{"role": "user", "content": "cache me"}},
			"max_tokens": 32,
		}
		status, body, headers := a.requestHeaders(t, http.MethodPost, "/v1/chat/completions", payload, a.apiKey)
		if status != 200 {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if headers.Get("X-Gogate-Cache") != "MISS" {
			t.Fatalf("first cache=%q", headers.Get("X-Gogate-Cache"))
		}
		if !strings.Contains(string(body), "cache me") {
			t.Fatalf("body=%s", body)
		}
		status, _, headers = a.requestHeaders(t, http.MethodPost, "/v1/chat/completions", payload, a.apiKey)
		if status != 200 || headers.Get("X-Gogate-Cache") != "HIT" {
			t.Fatalf("second status=%d cache=%q", status, headers.Get("X-Gogate-Cache"))
		}
	})

	t.Run("streaming", func(t *testing.T) {
		payload := map[string]any{
			"model": "mock", "stream": true,
			"stream_options": map[string]any{"include_usage": true},
			"messages":       []map[string]any{{"role": "user", "content": "stream me"}},
			"max_tokens":     16,
		}
		status, body, headers := a.requestHeaders(t, http.MethodPost, "/v1/chat/completions", payload, a.apiKey)
		if status != 200 {
			t.Fatalf("status=%d body=%s", status, body)
		}
		text := string(body)
		if !strings.Contains(text, "data: ") || !strings.Contains(text, "[DONE]") {
			t.Fatalf("bad SSE: %s", text)
		}
		if !strings.Contains(text, `"usage"`) {
			t.Fatalf("expected usage chunk: %s", text)
		}
		if headers.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("content-type=%q", headers.Get("Content-Type"))
		}
	})

	t.Run("pii redaction", func(t *testing.T) {
		payload := map[string]any{
			"model":      "mock",
			"messages":   []map[string]any{{"role": "user", "content": "contact alice@example.com"}},
			"max_tokens": 32,
		}
		status, body := a.request(t, http.MethodPost, "/v1/chat/completions", payload, a.apiKey)
		if status != 200 {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if strings.Contains(string(body), "alice@example.com") || !strings.Contains(string(body), "[REDACTED_EMAIL]") {
			t.Fatalf("pii not redacted: %s", body)
		}
	})

	t.Run("fallback before first byte", func(t *testing.T) {
		payload := map[string]any{
			"model":      "fallback",
			"messages":   []map[string]any{{"role": "user", "content": "fallback please"}},
			"max_tokens": 16,
		}
		status, _, headers := a.requestHeaders(t, http.MethodPost, "/v1/chat/completions", payload, a.apiKey)
		if status != 200 {
			t.Fatalf("status=%d", status)
		}
		if headers.Get("X-Gogate-Provider") != "fallback-good" {
			t.Fatalf("provider=%q", headers.Get("X-Gogate-Provider"))
		}
	})

	t.Run("rate limit 429", func(t *testing.T) {
		key, keyID := a.createKeyWithID(t, a.tenantID, "rl", nil)
		a.upsertQuota(t, map[string]any{"tenant_id": a.tenantID, "api_key_id": keyID, "rpm": 1, "tpm": 1000000})
		payload := map[string]any{"model": "mock", "messages": []map[string]any{{"role": "user", "content": "limit me"}}, "max_tokens": 8}
		status, _ := a.request(t, http.MethodPost, "/v1/chat/completions", payload, key)
		if status != 200 {
			t.Fatalf("first status=%d", status)
		}
		status, _, headers := a.requestHeaders(t, http.MethodPost, "/v1/chat/completions", payload, key)
		if status != 429 || headers.Get("Retry-After") == "" {
			t.Fatalf("second status=%d retry=%q", status, headers.Get("Retry-After"))
		}
	})

	t.Run("budget 429", func(t *testing.T) {
		if _, err := st.UpsertPrice(ctx, store.ModelPrice{Provider: "mock", Model: "mock", InputUSDPer1M: 1000000, OutputUSDPer1M: 1000000}); err != nil {
			t.Fatal(err)
		}
		if err := manager.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		tenant := a.createTenant(t, "budget")
		key := a.createKey(t, tenant, "budget", nil)
		a.upsertQuota(t, map[string]any{"tenant_id": tenant, "rpm": 100, "tpm": 1000000, "monthly_cost_usd": 0.000001})
		payload := map[string]any{"model": "mock", "messages": []map[string]any{{"role": "user", "content": "expensive"}}, "max_tokens": 16}
		status, body := a.request(t, http.MethodPost, "/v1/chat/completions", payload, key)
		if status != 429 || !strings.Contains(string(body), "insufficient_quota") {
			t.Fatalf("status=%d body=%s", status, body)
		}
	})

	t.Run("semantic cache", func(t *testing.T) {
		hash := []byte("hash-one")
		shortMax, longMax := 10, 1000
		paramsShort := cache.ParamsHash(cache.KeyInput{Model: "mock", MaxTokens: &shortMax})
		paramsLong := cache.ParamsHash(cache.KeyInput{Model: "mock", MaxTokens: &longMax})
		if err := semantic.Store(ctx, a.tenantID, "mock", "semantic hello world", paramsShort, "mock", hash, []byte(`{"id":"semantic"}`), []byte(`{}`), 0); err != nil {
			t.Fatal(err)
		}
		entry, err := semantic.Lookup(ctx, a.tenantID, "mock", "semantic hello world", paramsShort)
		if err != nil {
			t.Fatal(err)
		}
		if entry == nil || !strings.Contains(string(entry.Response), "semantic") {
			t.Fatalf("entry=%+v", entry)
		}
		wrong, err := semantic.Lookup(ctx, a.tenantID, "mock", "semantic hello world", paramsLong)
		if err != nil {
			t.Fatal(err)
		}
		if wrong != nil {
			t.Fatalf("semantic cache must respect generation params, got %+v", wrong)
		}
	})

	t.Run("admin provider CRUD", func(t *testing.T) {
		var created struct {
			ID string `json:"id"`
		}
		a.adminJSON(t, http.MethodPost, "/admin/v1/providers", map[string]any{
			"name": "admin-mock", "kind": "mock", "base_url": "internal://mock",
			"models": []string{"admin-mock"}, "priority": 50, "enabled": true,
			"timeout_ms": 5000, "max_retries": 0,
		}, &created)
		if created.ID == "" {
			t.Fatal("empty provider id")
		}
		status, body := a.request(t, http.MethodGet, "/v1/models", nil, a.apiKey)
		if status != 200 || !strings.Contains(string(body), "admin-mock") {
			t.Fatalf("models status=%d body=%s", status, body)
		}
		a.adminJSON(t, http.MethodDelete, "/admin/v1/providers/"+created.ID, nil, nil)
	})

	t.Run("public listener has no admin routes", func(t *testing.T) {
		status, _ := a.request(t, http.MethodGet, "/admin/v1/health", nil, a.apiKey)
		if status != http.StatusNotFound {
			t.Fatalf("status=%d", status)
		}
	})

	t.Run("admin validation", func(t *testing.T) {
		missingTenant := "00000000-0000-0000-0000-000000000000"
		status, _ := a.adminRequest(t, http.MethodPost, "/admin/v1/keys", map[string]any{"tenant_id": missingTenant, "name": "missing"})
		if status != http.StatusNotFound {
			t.Fatalf("missing tenant status=%d", status)
		}
		status, _ = a.adminRequest(t, http.MethodDelete, "/admin/v1/keys/not-a-uuid", nil)
		if status != http.StatusBadRequest {
			t.Fatalf("invalid uuid status=%d", status)
		}
		otherTenant := a.createTenant(t, "other")
		otherKey, _ := a.createKeyWithID(t, otherTenant, "other-key", nil)
		_ = otherKey
		otherKeyID := ""
		for _, k := range a.listKeys(t, otherTenant) {
			if k.Name == "other-key" {
				otherKeyID = k.ID
			}
		}
		status, _ = a.adminRequest(t, http.MethodPost, "/admin/v1/quotas", map[string]any{
			"tenant_id": a.tenantID, "api_key_id": otherKeyID, "rpm": 1,
		})
		if status != http.StatusBadRequest {
			t.Fatalf("cross-tenant quota status=%d", status)
		}
		status, _ = a.adminRequest(t, http.MethodPost, "/admin/v1/quotas", map[string]any{
			"tenant_id": a.tenantID, "rpm": -1,
		})
		if status != http.StatusBadRequest {
			t.Fatalf("negative quota status=%d", status)
		}
		status, _ = a.adminRequest(t, http.MethodPost, "/admin/v1/providers", map[string]any{
			"name": "unsafe-env", "kind": "mock", "models": []string{"unsafe"},
			"api_key_env": "AWS_SECRET_ACCESS_KEY",
		})
		if status != http.StatusBadRequest {
			t.Fatalf("unsafe api_key_env status=%d", status)
		}
	})

	t.Run("partial provider patch", func(t *testing.T) {
		var created struct {
			ID string `json:"id"`
		}
		a.adminJSON(t, http.MethodPost, "/admin/v1/providers", map[string]any{
			"name": "patch-me", "kind": "mock", "models": []string{"patch-me"},
			"priority": 50, "enabled": true, "timeout_ms": 5000, "max_retries": 0,
		}, &created)
		a.adminJSON(t, http.MethodPatch, "/admin/v1/providers/"+created.ID, map[string]any{"priority": 7}, nil)
		var providers []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Priority int    `json:"priority"`
			Enabled  bool   `json:"enabled"`
		}
		a.adminJSON(t, http.MethodGet, "/admin/v1/providers", nil, &providers)
		found := false
		for _, p := range providers {
			if p.ID == created.ID {
				found = true
				if p.Name != "patch-me" || p.Priority != 7 || !p.Enabled {
					t.Fatalf("patch result=%+v", p)
				}
			}
		}
		if !found {
			t.Fatal("provider not found after patch")
		}
		a.adminJSON(t, http.MethodDelete, "/admin/v1/providers/"+created.ID, nil, nil)
	})

	t.Run("usage survives key deletion", func(t *testing.T) {
		key, keyID := a.createKeyWithID(t, a.tenantID, "delete-me", nil)
		payload := map[string]any{"model": "mock", "messages": []map[string]any{{"role": "user", "content": "keep my usage"}}, "max_tokens": 8}
		status, body := a.request(t, http.MethodPost, "/v1/chat/completions", payload, key)
		if status != 200 {
			t.Fatalf("status=%d body=%s", status, body)
		}
		time.Sleep(200 * time.Millisecond)
		a.adminJSON(t, http.MethodDelete, "/admin/v1/keys/"+keyID, nil, nil)
		time.Sleep(200 * time.Millisecond)
		var count int
		if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM usage_daily WHERE api_key_id=$1`, keyID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatal("usage was deleted together with the key")
		}
	})

	t.Run("usage and audit persisted", func(t *testing.T) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			var usageCount, auditCount int
			_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM usage_daily WHERE tenant_id=$1`, a.tenantID).Scan(&usageCount)
			_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM request_audit WHERE tenant_id=$1`, a.tenantID).Scan(&auditCount)
			if usageCount > 0 && auditCount > 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("usage=%d audit=%d", usageCount, auditCount)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

func (a *app) createTenant(t *testing.T, name string) string {
	t.Helper()
	var out struct {
		ID string `json:"id"`
	}
	a.adminJSON(t, http.MethodPost, "/admin/v1/tenants", map[string]any{"name": name}, &out)
	if out.ID == "" {
		t.Fatal("empty tenant id")
	}
	return out.ID
}

func (a *app) createKey(t *testing.T, tenant, name string, expires *time.Time) string {
	t.Helper()
	key, _ := a.createKeyWithID(t, tenant, name, expires)
	return key
}

func (a *app) createKeyWithID(t *testing.T, tenant, name string, expires *time.Time) (string, string) {
	t.Helper()
	body := map[string]any{"tenant_id": tenant, "name": name}
	if expires != nil {
		body["expires_at"] = expires
	}
	var out struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	a.adminJSON(t, http.MethodPost, "/admin/v1/keys", body, &out)
	if out.Key == "" || out.ID == "" {
		t.Fatal("empty key or id")
	}
	return out.Key, out.ID
}

func (a *app) upsertQuota(t *testing.T, body map[string]any) {
	t.Helper()
	var out map[string]any
	a.adminJSON(t, http.MethodPost, "/admin/v1/quotas", body, &out)
}

func (a *app) adminRequest(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	req := a.newRequest(t, method, path, body, a.cfg.AdminToken)
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(a.admin.URL, "http://")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

type keyInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a *app) listKeys(t *testing.T, tenantID string) []keyInfo {
	t.Helper()
	var keys []keyInfo
	a.adminJSON(t, http.MethodGet, "/admin/v1/keys?tenant_id="+tenantID, nil, &keys)
	return keys
}

func (a *app) adminJSON(t *testing.T, method, path string, body any, out any) {
	t.Helper()
	req := a.newRequest(t, method, path, body, a.cfg.AdminToken)
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(a.admin.URL, "http://")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: status=%d body=%s", method, path, resp.StatusCode, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
	}
}

func (a *app) request(t *testing.T, method, path string, body any, apiKey string) (int, []byte) {
	t.Helper()
	status, raw, _ := a.requestHeaders(t, method, path, body, apiKey)
	return status, raw
}

func (a *app) requestHeaders(t *testing.T, method, path string, body any, apiKey string) (int, []byte, http.Header) {
	t.Helper()
	req := a.newRequest(t, method, path, body, apiKey)
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(a.public.URL, "http://")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header
}

func (a *app) newRequest(t *testing.T, method, path string, body any, token string) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://example"+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestKeyHelpers(t *testing.T) {
	mat, err := security.GenerateKey("test", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(mat.Plaintext, "gk_test_") {
		t.Fatalf("key=%s", mat.Plaintext)
	}
	if _, _, err := security.ParseKey(mat.Plaintext); err != nil {
		t.Fatal(err)
	}
}
