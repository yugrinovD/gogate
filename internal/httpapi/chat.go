package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"crypto/rand"
	"encoding/hex"

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
	id, _ := reqctx.IdentityFrom(r.Context())

	var req chatRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apierr.Write(w, r, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", "request body is too large")
			return
		}
		apierr.BadRequest(w, r, "invalid_json", "invalid JSON body")
		return
	}
	if err := ensureEOF(dec); err != nil {
		apierr.BadRequest(w, r, "invalid_json", "unexpected data after JSON body")
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
		return
	}
	if len(snap.Providers.ForModel(req.Model)) == 0 {
		apierr.NotFound(w, r, "model_not_found", fmt.Sprintf("model %q is not available", req.Model))
		h.finish(r, outcome{Model: req.Model, Status: http.StatusNotFound, Latency: time.Since(start), ErrorCode: "model_not_found", PII: piiRedacted})
		return
	}

	maxOut := h.maxOutput(req)
	_, reserveTokens, estimatedCost := h.estimate(snap, req.Model, messages, maxOut)
	limits := h.limits(snap, id)
	if !h.cfg.RateLimitEnabled {
		limits = nil
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

	cacheKey, promptHash, prompt := cache.Key(cache.KeyInput{
		TenantID: id.TenantID, Model: req.Model, Messages: messages,
		Temperature: req.Temperature, TopP: req.TopP, MaxTokens: req.MaxTokens, Stop: stop, Seed: req.Seed,
	})

	if !req.Stream {
		if cached := h.lookupExact(r.Context(), cacheKey); cached != nil {
			h.respondCached(w, r, start, req, cached, "HIT", limits, reserveTokens, estimatedCost, piiRedacted)
			return
		}
		if h.semantic != nil {
			entry, err := h.semantic.Lookup(r.Context(), id.TenantID, req.Model, prompt)
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
	} else if h.metrics != nil {
		h.metrics.IncCacheHit("bypass")
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
		h.stream(w, r, start, req, preq, id, limits, reserveTokens, estimatedCost, cacheKey, prompt, promptHash, piiRedacted)
		return
	}
	h.nonStream(w, r, start, req, preq, snap, id, limits, reserveTokens, estimatedCost, cacheKey, prompt, promptHash, piiRedacted)
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
		Status: http.StatusOK, Latency: time.Since(start), PII: piiRedacted,
	})
}

func (h *Handler) nonStream(w http.ResponseWriter, r *http.Request, start time.Time, req chatRequest, preq provider.Request, snap *control.Snapshot, id reqctx.Identity, limits []ratelimit.Limit, reservedTokens int, reservedCost float64, cacheKey, prompt string, promptHash []byte, piiRedacted bool) {
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
			h.settle(r.Context(), limits, reservedTokens, reservedCost, provider.Usage{}, 0)
			if h.metrics != nil {
				h.metrics.IncUpstreamError(sess.Provider(), errorKind(err))
			}
			if r.Context().Err() != nil {
				h.finish(r, outcome{Model: req.Model, Provider: sess.Provider(), Stream: false, Status: 499, Latency: time.Since(start), ErrorCode: "client_canceled", PII: piiRedacted})
				return
			}
			apierr.Upstream(w, r, "upstream_stream_error", "upstream response failed")
			h.finish(r, outcome{Model: req.Model, Provider: sess.Provider(), Stream: false, Status: http.StatusBadGateway, Latency: time.Since(start), ErrorCode: "upstream_stream_error", PII: piiRedacted})
			return
		}
		if chunk.ID != "" {
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
	price := snap.PriceFor(sess.Provider(), req.Model)
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
		if err := h.semantic.Store(sctx, id.TenantID, req.Model, prompt, sess.Provider(), promptHash, body, usageJSON, actualCost); err != nil {
			h.log.Warn("semantic cache store failed", "error", err)
		}
		cancel()
	}
	w.Header().Set("X-Gogate-Cache", "MISS")
	w.Header().Set("X-Gogate-Provider", sess.Provider())
	h.finish(r, outcome{Model: req.Model, Provider: sess.Provider(), Cache: "MISS", Stream: false, Status: http.StatusOK, Usage: *usage, CostUSD: actualCost, Latency: time.Since(start), PII: piiRedacted})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request, start time.Time, req chatRequest, preq provider.Request, id reqctx.Identity, limits []ratelimit.Limit, reservedTokens int, reservedCost float64, cacheKey, prompt string, promptHash []byte, piiRedacted bool) {
	sess, err := h.orch.Open(r.Context(), preq)
	if err != nil {
		h.writeOpenError(w, r, start, req, limits, reservedTokens, reservedCost, piiRedacted, err)
		return
	}
	defer func() { _ = sess.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.settle(r.Context(), limits, reservedTokens, reservedCost, provider.Usage{}, 0)
		apierr.Internal(w, r, "streaming is not supported by this server")
		h.finish(r, outcome{Model: req.Model, Provider: sess.Provider(), Stream: true, Status: http.StatusInternalServerError, Latency: time.Since(start), ErrorCode: "stream_unsupported", PII: piiRedacted})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Gogate-Cache", "BYPASS")
	w.Header().Set("X-Gogate-Provider", sess.Provider())
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	rc := http.NewResponseController(w)
	var content strings.Builder
	var usage *provider.Usage
	usageSent := false
	errCode := ""
	respID := newCompletionID()
	model := req.Model
	created := time.Now().Unix()
	first := true

	for {
		chunk, err := sess.Next(r.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			errCode = errorKind(err)
			if h.metrics != nil {
				h.metrics.IncUpstreamError(sess.Provider(), errCode)
			}
			if r.Context().Err() == nil {
				writeSSEError(w, "upstream response failed")
			}
			break
		}
		if chunk.ID != "" {
			respID = chunk.ID
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
		if respID == "" {
			out.ID = newCompletionID()
		}
		writeSSE(w, out)
		if h.cfg.StreamIdleTimeout > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(h.cfg.StreamIdleTimeout))
		}
		flusher.Flush()
	}

	if includeUsage && !usageSent {
		if usage == nil {
			u := totalUsage(tokenest.Prompt(preq.Messages), tokenest.Response(content.String()))
			usage = &u
		}
		u := toUsageDTO(*usage)
		writeSSE(w, chatChunk{ID: respID, Object: "chat.completion.chunk", Created: created, Model: model, Choices: []chunkChoiceDTO{}, Usage: &u})
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
	_ = rc.SetWriteDeadline(time.Time{})

	if usage == nil {
		u := totalUsage(tokenest.Prompt(preq.Messages), tokenest.Response(content.String()))
		usage = &u
	} else if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	price := control.Price{}
	if snap := h.control.Snapshot(); snap != nil {
		price = snap.PriceFor(sess.Provider(), req.Model)
	}
	actualCost := cost.Calculate(cost.Price{Input: price.InputUSDPer1M, Output: price.OutputUSDPer1M}, usage.PromptTokens, usage.CompletionTokens)
	h.settle(r.Context(), limits, reservedTokens, reservedCost, *usage, actualCost)
	status := http.StatusOK
	if r.Context().Err() != nil {
		errCode = "client_canceled"
	}
	h.finish(r, outcome{Model: req.Model, Provider: sess.Provider(), Cache: "BYPASS", Stream: true, Status: status, Usage: *usage, CostUSD: actualCost, Latency: time.Since(start), ErrorCode: errCode, PII: piiRedacted})
}

func (h *Handler) writeOpenError(w http.ResponseWriter, r *http.Request, start time.Time, req chatRequest, limits []ratelimit.Limit, reservedTokens int, reservedCost float64, piiRedacted bool, err error) {
	h.settle(r.Context(), limits, reservedTokens, reservedCost, provider.Usage{}, 0)
	status, code, msg := mapOpenError(err)
	if r.Context().Err() != nil {
		h.finish(r, outcome{Model: req.Model, Status: 499, Latency: time.Since(start), ErrorCode: "client_canceled", PII: piiRedacted})
		return
	}
	if h.metrics != nil {
		h.metrics.IncUpstreamError(upstreamProvider(err), errorKind(err))
	}
	switch status {
	case http.StatusNotFound:
		apierr.NotFound(w, r, code, msg)
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
	switch {
	case errors.Is(err, orchestrator.ErrModelNotFound):
		return http.StatusNotFound, "model_not_found", "model is not available"
	case errors.Is(err, orchestrator.ErrAllProvidersUnavailable):
		return http.StatusServiceUnavailable, "all_providers_unavailable", "all providers are unavailable"
	case errors.Is(err, context.DeadlineExceeded):
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

func errorKind(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "error"
}

func writeSSE(w io.Writer, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(body)
	_, _ = io.WriteString(w, "\n\n")
}

func writeSSEError(w io.Writer, message string) {
	body, _ := json.Marshal(apierr.Envelope{Error: apierr.Error{Message: message, Type: "upstream_error", Code: "upstream_stream_error"}})
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(body)
	_, _ = io.WriteString(w, "\n\n")
}

func newCompletionID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return "chatcmpl-" + hex.EncodeToString(b)
}
