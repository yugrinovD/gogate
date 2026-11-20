// Package httpapi implements the public OpenAI-compatible API.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/yugrinovD/gogate/internal/cache"
	"github.com/yugrinovD/gogate/internal/config"
	"github.com/yugrinovD/gogate/internal/control"
	"github.com/yugrinovD/gogate/internal/cost"
	"github.com/yugrinovD/gogate/internal/observability"
	"github.com/yugrinovD/gogate/internal/orchestrator"
	"github.com/yugrinovD/gogate/internal/provider"
	"github.com/yugrinovD/gogate/internal/ratelimit"
	"github.com/yugrinovD/gogate/internal/reqctx"
	"github.com/yugrinovD/gogate/internal/store"
	"github.com/yugrinovD/gogate/internal/tokenest"
	"github.com/yugrinovD/gogate/internal/usage"
)

// Handler serves the public API.
type Handler struct {
	cfg      config.Config
	control  *control.Manager
	orch     *orchestrator.Orchestrator
	limiter  *ratelimit.Limiter
	exact    *cache.Exact
	semantic *cache.Semantic
	usage    *usage.Writer
	metrics  *observability.Metrics
	log      *slog.Logger
	started  time.Time
}

// Options contains public API dependencies.
type Options struct {
	Config   config.Config
	Control  *control.Manager
	Orch     *orchestrator.Orchestrator
	Limiter  *ratelimit.Limiter
	Exact    *cache.Exact
	Semantic *cache.Semantic
	Usage    *usage.Writer
	Metrics  *observability.Metrics
	Logger   *slog.Logger
}

// New creates a public API handler.
func New(o Options) *Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Handler{
		cfg:      o.Config,
		control:  o.Control,
		orch:     o.Orch,
		limiter:  o.Limiter,
		exact:    o.Exact,
		semantic: o.Semantic,
		usage:    o.Usage,
		metrics:  o.Metrics,
		log:      o.Logger,
		started:  time.Now(),
	}
}

// Models handles GET /v1/models.
func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	snap := h.control.Snapshot()
	data := make([]modelObject, 0)
	if snap != nil {
		for _, m := range snap.Providers.Models() {
			data = append(data, modelObject{
				ID: m.ID, Object: "model", Created: h.started.Unix(), OwnedBy: m.OwnedBy,
			})
		}
	}
	writeJSON(w, http.StatusOK, modelList{Object: "list", Data: data})
}

func (h *Handler) limits(snap *control.Snapshot, id reqctx.Identity) []ratelimit.Limit {
	base := "{t:" + id.TenantID + "}:"
	var out []ratelimit.Limit
	if q, ok := snap.TenantQuotas[id.TenantID]; ok {
		out = append(out, toLimit(base+"tenant", q, h.cfg.BudgetEnabled))
	}
	if q, ok := snap.KeyQuotas[id.KeyID]; ok {
		out = append(out, toLimit(base+"key:"+id.KeyID, q, h.cfg.BudgetEnabled))
	}
	filtered := out[:0]
	for _, l := range out {
		if l.RPM != nil || l.TPM != nil || l.DailyRequests != nil || l.DailyTokens != nil || l.MonthlyCostUSD != nil {
			filtered = append(filtered, l)
		}
	}
	return filtered
}

func toLimit(id string, q control.Quota, budgetEnabled bool) ratelimit.Limit {
	monthly := q.MonthlyCostUSD
	if !budgetEnabled {
		monthly = nil
	}
	return ratelimit.Limit{
		ID:             id,
		RPM:            q.RPM,
		TPM:            q.TPM,
		DailyRequests:  q.DailyRequests,
		DailyTokens:    q.DailyTokens,
		MonthlyCostUSD: monthly,
	}
}

func (h *Handler) maxOutput(req chatRequest) int {
	maxOut := h.cfg.MaxOutputTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 && *req.MaxTokens < maxOut {
		maxOut = *req.MaxTokens
	}
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 && *req.MaxCompletionTokens < maxOut {
		maxOut = *req.MaxCompletionTokens
	}
	return maxOut
}

func (h *Handler) estimate(snap *control.Snapshot, model string, messages []provider.Message, maxOut int) (promptTokens, reserveTokens int, estimatedCost float64, priceKnown bool) {
	promptTokens = tokenest.Prompt(messages)
	reserveTokens = promptTokens + maxOut
	chain := snap.Providers.ForModel(model)
	if len(chain) > 0 {
		price, ok := snap.PriceFor(chain[0].Name(), model)
		priceKnown = ok
		if ok {
			estimatedCost = cost.Calculate(cost.Price{Input: price.InputUSDPer1M, Output: price.OutputUSDPer1M}, promptTokens, maxOut)
		}
	}
	return
}

func (h *Handler) settle(ctx context.Context, limits []ratelimit.Limit, reservedTokens int, reservedCost float64, actual provider.Usage, actualCost float64) {
	if len(limits) == 0 {
		return
	}
	if ctx.Err() != nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	deltaTokens := actual.PromptTokens + actual.CompletionTokens - reservedTokens
	deltaCost := actualCost - reservedCost
	if err := h.limiter.Settle(ctx, limits, deltaTokens, deltaCost); err != nil {
		h.log.Warn("rate limit settlement failed", "error", err)
	}
}

// outcome carries fields needed by metrics, usage and audit.
type outcome struct {
	Model     string
	Provider  string
	Cache     string
	Stream    bool
	Status    int
	Usage     provider.Usage
	CostUSD   float64
	Latency   time.Duration
	ErrorCode string
	PII       bool
}

func (h *Handler) finish(r *http.Request, o outcome) {
	id, _ := reqctx.IdentityFrom(r.Context())
	if h.metrics != nil {
		h.metrics.IncRequest(o.Provider, o.Model, o.Status)
		h.metrics.ObserveDuration(o.Provider, o.Model, o.Stream, o.Latency)
		h.metrics.AddTokens("prompt", o.Usage.PromptTokens)
		h.metrics.AddTokens("completion", o.Usage.CompletionTokens)
		h.metrics.AddCost(o.CostUSD)
	}
	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if h.usage != nil && id.TenantID != "" {
		h.usage.SubmitUsage(store.UsageRecord{
			TenantID:         id.TenantID,
			APIKeyID:         id.KeyID,
			Provider:         o.Provider,
			Model:            o.Model,
			Day:              day,
			Requests:         1,
			PromptTokens:     int64(o.Usage.PromptTokens),
			CompletionTokens: int64(o.Usage.CompletionTokens),
			CostUSD:          o.CostUSD,
		})
		h.usage.TouchKey(id.KeyID)
		if h.cfg.AuditEnabled {
			h.usage.SubmitAudit(store.AuditRecord{
				RequestID:        reqctx.RequestID(r.Context()),
				TenantID:         id.TenantID,
				KeyPrefix:        id.KeyPrefix,
				Model:            o.Model,
				Provider:         o.Provider,
				Stream:           o.Stream,
				Status:           o.Status,
				Cache:            o.Cache,
				PromptTokens:     o.Usage.PromptTokens,
				CompletionTokens: o.Usage.CompletionTokens,
				CostUSD:          o.CostUSD,
				LatencyMS:        int(o.Latency.Milliseconds()),
				ErrorCode:        o.ErrorCode,
				PIIRedacted:      o.PII,
			})
		}
	}
	h.log.Info("chat request completed",
		"tenant_id", id.TenantID,
		"key_prefix", id.KeyPrefix,
		"model", o.Model,
		"provider", o.Provider,
		"stream", o.Stream,
		"status", o.Status,
		"cache", o.Cache,
		"prompt_tokens", o.Usage.PromptTokens,
		"completion_tokens", o.Usage.CompletionTokens,
		"cost_usd", o.CostUSD,
		"latency_ms", o.Latency.Milliseconds(),
		"error_code", o.ErrorCode,
		"pii_redacted", o.PII,
	)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func totalUsage(prompt, completion int) provider.Usage {
	return provider.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
}

func hasMonthlyBudget(limits []ratelimit.Limit) bool {
	for _, l := range limits {
		if l.MonthlyCostUSD != nil {
			return true
		}
	}
	return false
}

func (h *Handler) firstProvider(snap *control.Snapshot, model string) string {
	chain := snap.Providers.ForModel(model)
	if len(chain) == 0 {
		return ""
	}
	return chain[0].Name()
}
