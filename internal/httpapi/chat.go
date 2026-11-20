package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/yugrinovD/gogate/internal/apierr"
	"github.com/yugrinovD/gogate/internal/cache"
	"github.com/yugrinovD/gogate/internal/control"
	"github.com/yugrinovD/gogate/internal/cost"
	"github.com/yugrinovD/gogate/internal/observability"
	"github.com/yugrinovD/gogate/internal/orchestrator"
	"github.com/yugrinovD/gogate/internal/pii"
	"github.com/yugrinovD/gogate/internal/provider"
	"github.com/yugrinovD/gogate/internal/ratelimit"
	"github.com/yugrinovD/gogate/internal/reqctx"
	"github.com/yugrinovD/gogate/internal/tokenest"
)

// ChatCompletions handles POST /v1/chat/completions.
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx, span := observability.Tracer().Start(r.Context(), "gateway.chat")
	defer span.End()
	r = r.WithContext(ctx)
	if h.cfg.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(r.Context(), h.cfg.RequestTimeout)
		defer cancel()
		r = r.WithContext(ctx)
	}
	id, _ := reqctx.IdentityFrom(r.Context())

	var req chatRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apierr.Write(w, r, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", "request body is too large")
			h.finish(r, outcome{Status: http.StatusRequestEntityTooLarge, Latency: time.Since(start), ErrorCode: "request_too_large"})
			return
		}
		apierr.BadRequest(w, r, "invalid_json", "invalid JSON body")
		h.finish(r, outcome{Status: http.StatusBadRequest, Latency: time.Since(start), ErrorCode: "invalid_json"})
		return
	}
	if err := ensureEOF(dec); err != nil {
		apierr.BadRequest(w, r, "invalid_json", "unexpected data after JSON body")
		h.finish(r, outcome{Status: http.StatusBadRequest, Latency: time.Since(start), ErrorCode: "invalid_json"})
		return
	}
	if errCode, msg := h.validate(&req); errCode != "" {
		apierr.BadRequest(w, r, errCode, msg)
		h.finish(r, outcome{Model: req.Model, Status: http.StatusBadRequest, Latency: time.Since(start), ErrorCode: errCode})
		return
	}
	span.SetAttributes(attribute.String("model", req.Model), attribute.Bool("stream", req.Stream), attribute.String("tenant_id", id.TenantID))
	messages, err := parseMessages(req.Messages)
	if err != nil {
		apierr.BadRequest(w, r, "invalid_messages", err.Error())
		h.finish(r, outcome{Model: req.Model, Status: http.StatusBadRequest, Latency: time.Since(start), ErrorCode: "invalid_messages"})
		return
	}
	stop, err := parseStop(req.Stop)
	if err != nil {
		apierr.BadRequest(w, r, "invalid_stop", err.Error())
		h.finish(r, outcome{Model: req.Model, Status: http.StatusBadRequest, Latency: time.Since(start), ErrorCode: "invalid_stop"})
		return
	}
	piiRedacted := false
	if h.cfg.PIIRedactionEnabled {
		messages, piiRedacted = pii.RedactMessages(messages)
	}

	snap := h.control.Snapshot()
	if snap == nil {
		apierr.Unavailable(w, r, "not_ready", "gateway is not ready")
		h.finish(r, outcome{Model: req.Model, Status: http.StatusServiceUnavailable, Latency: time.Since(start), ErrorCode: "not_ready", PII: piiRedacted})
		return
	}
	if len(snap.Providers.ForModel(req.Model)) == 0 {
		apierr.NotFound(w, r, "model_not_found", fmt.Sprintf("model %q is not available", req.Model))
		h.finish(r, outcome{Model: req.Model, Status: http.StatusNotFound, Latency: time.Since(start), ErrorCode: "model_not_found", PII: piiRedacted})
		return
	}

	maxOut := h.maxOutput(req)
	limits := h.limits(snap, id)
	if !h.cfg.RateLimitEnabled {
		limits = nil
	}
	_, reserveTokens, estimatedCost, priceKnown := h.estimate(snap, req.Model, messages, maxOut)
	if h.cfg.BudgetEnabled && hasMonthlyBudget(limits) && !priceKnown {
		if h.metrics != nil {
			h.metrics.IncPricingMissing(h.firstProvider(snap, req.Model), req.Model)
		}
		apierr.Unavailable(w, r, "pricing_unavailable", "pricing is not configured for this model")
		h.finish(r, outcome{Model: req.Model, Status: http.StatusServiceUnavailable, Latency: time.Since(start), ErrorCode: "pricing_unavailable", PII: piiRedacted})
		return
	}

	admitCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	decision, err := h.limiter.Admit(admitCtx, limits, reserveTokens, estimatedCost)
	cancel()
	if err != nil {
		h.log.Error("rate limit check failed", "error", err)
		if !h.cfg.RateLimitFailOpen {
			apierr.Unavailable(w, r, "rate_limit_unavailable", "rate limiter is unavailable")
			h.finish(r, outcome{Model: req.Model, Status: http.StatusServiceUnavailable, Latency: time.Since(start), ErrorCode: "rate_limit_unavailable", PII: piiRedacted})
			return
		}
		limits = nil
		reserveTokens = 0
		estimatedCost = 0
	} else if !decision.Allowed {
		code := "rate_limit_exceeded"
		if decision.Reason == "monthly_budget" {
			code = "insufficient_quota"
			if h.metrics != nil {
				h.metrics.IncBudgetExceeded()
			}
		}
		if h.metrics != nil {
			h.metrics.IncRateLimited(decision.Reason)
		}
		msg := fmt.Sprintf("limit exceeded: %s", decision.Reason)
		apierr.RateLimited(w, r, code, msg, ratelimit.RetryAfterHeader(decision.RetryAfter))
		h.finish(r, outcome{Model: req.Model, Status: http.StatusTooManyRequests, Latency: time.Since(start), ErrorCode: decision.Reason, PII: piiRedacted})
		return
	}

	keyInput := cache.KeyInput{
		TenantID: id.TenantID, Model: req.Model, Messages: messages,
		Temperature: req.Temperature, TopP: req.TopP, MaxTokens: req.MaxTokens,
		MaxCompletion: req.MaxCompletionTokens, Stop: stop, Seed: req.Seed,
		PresencePenalty: req.PresencePenalty, FrequencyPenalty: req.FrequencyPenalty,
		PIIRedaction: h.cfg.PIIRedactionEnabled,
	}
	cacheKey, promptHash, prompt := cache.Key(keyInput)
	paramsHash := cache.ParamsHash(keyInput)

	if !req.Stream {
		if cached := h.lookupExact(r.Context(), cacheKey); cached != nil {
			h.respondCached(w, r, start, req, cached, "HIT", limits, reserveTokens, estimatedCost, piiRedacted)
			return
		}
		if h.semantic != nil {
			entry, err := h.semantic.Lookup(r.Context(), id.TenantID, req.Model, prompt, paramsHash)
			if err != nil {
				h.log.Warn("semantic cache lookup failed", "error", err)
			} else if entry != nil {
				var usage provider.Usage
				_ = json.Unmarshal(entry.Usage, &usage)
				cached := &cache.CachedResponse{Response: entry.Response, Usage: usage, Provider: entry.Provider, CostUSD: entry.CostUSD}
				h.respondCached(w, r, start, req, cached, "SEMANTIC", limits, reserveTokens, estimatedCost, piiRedacted)
				return
			}
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens == nil {
		maxTokens = req.MaxCompletionTokens
	}
	preq := provider.Request{
		Model:            req.Model,
		Messages:         messages,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		MaxTokens:        maxTokens,
		Stop:             stop,
		Seed:             req.Seed,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		Stream:           req.Stream,
	}
	if req.Stream {
		h.stream(w, r, start, req, preq, id, limits, reserveTokens, estimatedCost, cacheKey, prompt, promptHash, paramsHash, piiRedacted)
		return
	}
	h.nonStream(w, r, start, req, preq, snap, id, limits, reserveTokens, estimatedCost, cacheKey, prompt, promptHash, paramsHash, piiRedacted)
}

func (h *Handler) validate(req *chatRequest) (code, message string) {
	if strings.TrimSpace(req.Model) == "" || len(req.Model) > 128 {
		return "invalid_model", "model is required and must be at most 128 characters"
	}
	if len(req.Messages) == 0 {
		return "invalid_messages", "messages must not be empty"
	}
	if len(req.Messages) > h.cfg.MaxMessages {
		return "too_many_messages", fmt.Sprintf("at most %d messages are allowed", h.cfg.MaxMessages)
	}
	if req.N != nil && *req.N != 1 {
		return "unsupported_n", "only n=1 is supported"
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return "invalid_temperature", "temperature must be between 0 and 2"
	}
	if req.TopP != nil && (*req.TopP < 0 || *req.TopP > 1) {
		return "invalid_top_p", "top_p must be between 0 and 1"
	}
	if req.MaxTokens != nil && (*req.MaxTokens <= 0 || *req.MaxTokens > h.cfg.MaxOutputTokens) {
		return "invalid_max_tokens", fmt.Sprintf("max_tokens must be between 1 and %d", h.cfg.MaxOutputTokens)
	}
	if req.MaxCompletionTokens != nil && (*req.MaxCompletionTokens <= 0 || *req.MaxCompletionTokens > h.cfg.MaxOutputTokens) {
		return "invalid_max_tokens", fmt.Sprintf("max_completion_tokens must be between 1 and %d", h.cfg.MaxOutputTokens)
	}
	if req.PresencePenalty != nil && (*req.PresencePenalty < -2 || *req.PresencePenalty > 2) {
		return "invalid_presence_penalty", "presence_penalty must be between -2 and 2"
	}
	if req.FrequencyPenalty != nil && (*req.FrequencyPenalty < -2 || *req.FrequencyPenalty > 2) {
		return "invalid_frequency_penalty", "frequency_penalty must be between -2 and 2"
	}
	return "", ""
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("extra JSON value")
	}
	return err
}

func (h *Handler) lookupExact(ctx context.Context, key string) *cache.CachedResponse {
	if h.exact == nil {
		return nil
	}
	cached, err := h.exact.Get(ctx, key)
	if err != nil {
		h.log.Warn("exact cache lookup failed", "error", err)
		return nil
	}
	return cached
}

func (h *Handler) respondCached(w http.ResponseWriter, r *http.Request, start time.Time, req chatRequest, cached *cache.CachedResponse, kind string, limits []ratelimit.Limit, reservedTokens int, reservedCost float64, piiRedacted bool) {
	h.settle(r.Context(), limits, reservedTokens, reservedCost, provider.Usage{}, 0)
	if h.metrics != nil {
		h.metrics.IncCacheHit(strings.ToLower(kind))
	}
	w.Header().Set("X-Gogate-Cache", kind)
	if cached.Provider != "" {
		w.Header().Set("X-Gogate-Provider", cached.Provider)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cached.Response)
	h.finish(r, outcome{
		Model: req.Model, Provider: cached.Provider, Cache: kind, Stream: false,
		Status: http.StatusOK, Usage: cached.Usage, CostUSD: 0, Latency: time.Since(start), PII: piiRedacted,
	})
}

func (h *Handler) nonStream(w http.ResponseWriter, r *http.Request, start time.Time, req chatRequest, preq provider.Request, snap *control.Snapshot, id reqctx.Identity, limits []ratelimit.Limit, reservedTokens int, reservedCost float64, cacheKey, prompt string, promptHash, paramsHash []byte, piiRedacted bool) {
	sess, err := h.orch.Open(r.Context(), preq)
	if err != nil {
		h.writeOpenError(w, r, start, req, limits, reservedTokens, reservedCost, piiRedacted, err)
		return
	}
	defer func() { _ = sess.Close() }()

	var content strings.Builder
	var usage *provider.Usage
	finishReason := "stop"
	respID := ""
	model := req.Model
	created := time.Now().Unix()
	firstRole := "assistant"

	for {
		chunk, err := sess.Next(r.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if h.metrics != nil {
				h.metrics.IncUpstreamError(sess.Provider(), orchestrator.ErrorKind(err))
			}
			h.writeOpenError(w, r, start, req, limits, reservedTokens, reservedCost, piiRedacted, err)
			return
		}
		if respID == "" && chunk.ID != "" {
			respID = chunk.ID
		}
		if chunk.Created > 0 {
			created = chunk.Created
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Role != "" {
			firstRole = chunk.Role
		}
		content.WriteString(chunk.Content)
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
	}

	text := content.String()
	if usage == nil {
		u := totalUsage(tokenest.Prompt(preq.Messages), tokenest.Response(text))
		usage = &u
	} else if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if h.cfg.PIIRedactionEnabled {
		redacted, n := pii.RedactText(text)
		if n > 0 {
			text = redacted
			piiRedacted = true
		}
	}
	if respID == "" {
		respID = newCompletionID()
	}
	price, priceKnown := snap.PriceFor(sess.Provider(), req.Model)
	if !priceKnown && h.metrics != nil {
		h.metrics.IncPricingMissing(sess.Provider(), req.Model)
	}
	actualCost := cost.Calculate(cost.Price{Input: price.InputUSDPer1M, Output: price.OutputUSDPer1M}, usage.PromptTokens, usage.CompletionTokens)
	h.settle(r.Context(), limits, reservedTokens, reservedCost, *usage, actualCost)

	resp := chatResponse{
		ID:      respID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []choiceDTO{{
			Index:        0,
			Message:      messageDTO{Role: firstRole, Content: text},
			FinishReason: finishReason,
		}},
		Usage: toUsageDTO(*usage),
	}
	body, _ := json.Marshal(resp)
	if h.exact != nil {
		if err := h.exact.Set(r.Context(), cacheKey, cache.CachedResponse{Response: body, Usage: *usage, Provider: sess.Provider(), CostUSD: actualCost}); err != nil {
			h.log.Warn("exact cache store failed", "error", err)
		}
	}
	if h.semantic != nil {
		usageJSON, _ := json.Marshal(*usage)
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := h.semantic.Store(sctx, id.TenantID, req.Model, prompt, paramsHash, sess.Provider(), promptHash, body, usageJSON, actualCost); err != nil {
			h.log.Warn("semantic cache store failed", "error", err)
		}
		cancel()
	}
	w.Header().Set("X-Gogate-Cache", "MISS")
	w.Header().Set("X-Gogate-Provider", sess.Provider())
	h.finish(r, outcome{Model: req.Model, Provider: sess.Provider(), Cache: "MISS", Stream: false, Status: http.StatusOK, Usage: *usage, CostUSD: actualCost, Latency: time.Since(start), PII: piiRedacted})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request, start time.Time, req chatRequest, preq provider.Request, id reqctx.Identity, limits []ratelimit.Limit, reservedTokens int, reservedCost float64, cacheKey, prompt string, promptHash, paramsHash []byte, piiRedacted bool) {
	sess, err := h.orch.Open(r.Context(), preq)
	if err != nil {
		h.writeOpenError(w, r, start, req, limits, reservedTokens, reservedCost, piiRedacted, err)
		return
	}
	defer func() { _ = sess.Close() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Gogate-Cache", "BYPASS")
	w.Header().Set("X-Gogate-Provider", sess.Provider())

	rc := http.NewResponseController(w)
	if h.cfg.StreamIdleTimeout > 0 {
		_ = rc.SetWriteDeadline(time.Now().Add(h.cfg.StreamIdleTimeout))
	}
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		_ = sess.Close()
		h.settle(r.Context(), limits, reservedTokens, reservedCost, provider.Usage{}, 0)
		h.finish(r, outcome{Model: req.Model, Provider: sess.Provider(), Stream: true, Status: 499, Latency: time.Since(start), ErrorCode: "client_disconnected", PII: piiRedacted})
		return
	}

	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	var content strings.Builder
	var usage *provider.Usage
	usageSent := false
	errCode := ""
	writeFailed := false
	finalWritten := false
	respID := ""
	model := req.Model
	created := time.Now().Unix()
	first := true

	for {
		if ctxErr := r.Context().Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				errCode = "timeout"
				writeTimeoutFinal(w, rc)
				finalWritten = true
			} else {
				errCode = "client_canceled"
				writeFailed = true
			}
			break
		}
		chunk, err := sess.Next(r.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			errCode = orchestrator.ErrorKind(err)
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				errCode = "timeout"
			}
			if h.metrics != nil {
				h.metrics.IncUpstreamError(sess.Provider(), errCode)
			}
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				errCode = "timeout"
				writeTimeoutFinal(w, rc)
				finalWritten = true
			} else if r.Context().Err() == nil {
				if werr := writeSSEError(w, "upstream response failed"); werr != nil {
					writeFailed = true
				}
			} else {
				writeFailed = true
			}
			break
		}
		if respID == "" {
			if chunk.ID != "" {
				respID = chunk.ID
			} else {
				respID = newCompletionID()
			}
		}
		if chunk.Created > 0 {
			created = chunk.Created
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		content.WriteString(chunk.Content)
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		out := chatChunk{ID: respID, Object: "chat.completion.chunk", Created: created, Model: model}
		delta := deltaDTO{Content: chunk.Content}
		if first && chunk.Role != "" {
			delta.Role = chunk.Role
		} else if first {
			delta.Role = "assistant"
		}
		first = false
		choice := chunkChoiceDTO{Index: chunk.Index, Delta: delta}
		if chunk.FinishReason != "" {
			fr := chunk.FinishReason
			choice.FinishReason = &fr
		}
		out.Choices = []chunkChoiceDTO{choice}
		if includeUsage && chunk.Usage != nil {
			u := toUsageDTO(*chunk.Usage)
			out.Usage = &u
			usageSent = true
		}
		if h.cfg.StreamIdleTimeout > 0 {
			if derr := rc.SetWriteDeadline(time.Now().Add(h.cfg.StreamIdleTimeout)); derr != nil && !errors.Is(derr, http.ErrNotSupported) {
				errCode = "write_deadline"
				writeFailed = true
				break
			}
		}
		if err := writeSSE(w, out); err != nil {
			errCode = "client_disconnected"
			writeFailed = true
			break
		}
		if err := rc.Flush(); err != nil {
			errCode = "client_disconnected"
			writeFailed = true
			break
		}
	}

	if respID == "" {
		respID = newCompletionID()
	}
	if !writeFailed && !finalWritten && includeUsage && !usageSent {
		if usage == nil {
			u := totalUsage(tokenest.Prompt(preq.Messages), tokenest.Response(content.String()))
			usage = &u
		}
		u := toUsageDTO(*usage)
		if err := writeSSE(w, chatChunk{ID: respID, Object: "chat.completion.chunk", Created: created, Model: model, Choices: []chunkChoiceDTO{}, Usage: &u}); err != nil {
			writeFailed = true
		}
	}
	if !writeFailed && !finalWritten {
		if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
			writeFailed = true
		} else if err := rc.Flush(); err != nil {
			writeFailed = true
		}
	}
	_ = rc.SetWriteDeadline(time.Time{})

	if usage == nil {
		u := totalUsage(tokenest.Prompt(preq.Messages), tokenest.Response(content.String()))
		usage = &u
	} else if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	price := control.Price{}
	if snap := h.control.Snapshot(); snap != nil {
		var known bool
		price, known = snap.PriceFor(sess.Provider(), req.Model)
		if !known && h.metrics != nil {
			h.metrics.IncPricingMissing(sess.Provider(), req.Model)
		}
	}
	actualCost := cost.Calculate(cost.Price{Input: price.InputUSDPer1M, Output: price.OutputUSDPer1M}, usage.PromptTokens, usage.CompletionTokens)
	h.settle(r.Context(), limits, reservedTokens, reservedCost, *usage, actualCost)
	status := http.StatusOK
	if writeFailed {
		if errCode == "" {
			errCode = "client_disconnected"
		}
		if errCode != "timeout" {
			status = 499
		}
	}
	h.finish(r, outcome{Model: req.Model, Provider: sess.Provider(), Cache: "BYPASS", Stream: true, Status: status, Usage: *usage, CostUSD: actualCost, Latency: time.Since(start), ErrorCode: errCode, PII: piiRedacted})
}

func (h *Handler) writeOpenError(w http.ResponseWriter, r *http.Request, start time.Time, req chatRequest, limits []ratelimit.Limit, reservedTokens int, reservedCost float64, piiRedacted bool, err error) {
	h.settle(r.Context(), limits, reservedTokens, reservedCost, provider.Usage{}, 0)
	mapErr := err
	if errors.Is(r.Context().Err(), context.DeadlineExceeded) && !errors.Is(err, orchestrator.ErrFirstByteTimeout) {
		mapErr = context.DeadlineExceeded
	}
	status, code, msg := mapOpenError(mapErr)
	if r.Context().Err() != nil && !errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		h.finish(r, outcome{Model: req.Model, Status: 499, Latency: time.Since(start), ErrorCode: "client_canceled", PII: piiRedacted})
		return
	}
	var ue *provider.UpstreamError
	if errors.As(err, &ue) && ue.RetryAfter > 0 {
		w.Header().Set("Retry-After", ratelimit.RetryAfterHeader(ue.RetryAfter))
	}
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		apierr.Write(w, r, status, "invalid_request_error", code, msg)
	case http.StatusTooManyRequests:
		apierr.RateLimited(w, r, code, msg, w.Header().Get("Retry-After"))
	case http.StatusServiceUnavailable:
		apierr.Unavailable(w, r, code, msg)
	case http.StatusGatewayTimeout:
		apierr.Timeout(w, r, msg)
	default:
		apierr.Upstream(w, r, code, msg)
	}
	h.finish(r, outcome{Model: req.Model, Provider: upstreamProvider(err), Status: status, Latency: time.Since(start), ErrorCode: code, PII: piiRedacted})
}

func mapOpenError(err error) (int, string, string) {
	var ue *provider.UpstreamError
	if errors.As(err, &ue) {
		switch {
		case ue.StatusCode == http.StatusTooManyRequests:
			return http.StatusTooManyRequests, "rate_limit_exceeded", "upstream rate limit exceeded"
		case ue.StatusCode == http.StatusBadRequest, ue.StatusCode == http.StatusNotFound,
			ue.StatusCode == http.StatusRequestEntityTooLarge, ue.StatusCode == http.StatusUnprocessableEntity:
			return ue.StatusCode, "upstream_invalid_request", "upstream rejected the request"
		case ue.StatusCode == http.StatusUnauthorized || ue.StatusCode == http.StatusForbidden:
			return http.StatusBadGateway, "upstream_auth_error", "upstream rejected gateway credentials"
		case ue.StatusCode >= 500:
			return http.StatusBadGateway, "upstream_error", "upstream failed before the first byte"
		}
	}
	switch {
	case errors.Is(err, orchestrator.ErrModelNotFound):
		return http.StatusNotFound, "model_not_found", "model is not available"
	case errors.Is(err, orchestrator.ErrAllProvidersUnavailable):
		return http.StatusServiceUnavailable, "all_providers_unavailable", "all providers are unavailable"
	case errors.Is(err, orchestrator.ErrFirstByteTimeout), errors.Is(err, orchestrator.ErrStreamIdle), errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "upstream_timeout", "upstream timed out before the first byte"
	default:
		return http.StatusBadGateway, "upstream_error", "all providers failed before the first byte"
	}
}

func upstreamProvider(err error) string {
	var ue *provider.UpstreamError
	if errors.As(err, &ue) {
		return ue.Provider
	}
	return ""
}

func writeSSE(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}

func writeSSEError(w io.Writer, message string) error {
	return writeSSEErrorCode(w, message, "upstream_stream_error")
}

func writeSSEErrorCode(w io.Writer, message, code string) error {
	body, _ := json.Marshal(apierr.Envelope{Error: apierr.Error{Message: message, Type: "upstream_error", Code: code}})
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n\n")
	return err
}

func writeTimeoutFinal(w io.Writer, rc *http.ResponseController) {
	if rc != nil {
		_ = rc.SetWriteDeadline(time.Now().Add(2 * time.Second))
	}
	_ = writeSSEErrorCode(w, "request timeout", "request_timeout")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if rc != nil {
		_ = rc.Flush()
	}
}

func newCompletionID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return "chatcmpl-" + hex.EncodeToString(b)
}
