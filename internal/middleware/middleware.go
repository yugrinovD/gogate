// Package middleware contains HTTP middleware shared by public and admin APIs.
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	mathrand "math/rand/v2"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/yugrinovD/gogate/internal/apierr"
	"github.com/yugrinovD/gogate/internal/logging"
	"github.com/yugrinovD/gogate/internal/reqctx"
)

// RequestID assigns or validates a request id and installs a request logger.
func RequestID(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
			if id == "" || len(id) > 128 || !safeRequestID(id) {
				id = newRequestID()
			}
			w.Header().Set("X-Request-ID", id)
			ctx := reqctx.WithRequestID(r.Context(), id)
			ctx = logging.WithLogger(ctx, log.With("request_id", id))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func safeRequestID(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b)
}

// Recover catches panics and returns a JSON 500.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := NewRecorder(w)
			defer func() {
				if v := recover(); v != nil {
					logging.FromContext(r.Context()).Error("panic recovered", "panic", v, "stack", string(debug.Stack()))
					if !rec.WroteHeader() {
						apierr.Internal(rec, r, "internal server error")
					}
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// BodyLimit rejects requests with a declared body larger than max and wraps the
// body so reads cannot exceed the limit.
func BodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				apierr.Write(w, r, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", "request body is too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog emits one structured line per request. Prompts and responses are
// never included. sample < 1 reduces volume while still logging errors.
func AccessLog(log *slog.Logger, sample float64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := NewRecorder(w)
			next.ServeHTTP(rec, r)
			latency := time.Since(start)
			status := rec.Status()
			// Chat handler emits a richer completion line with provider, cache,
			// token and cost fields. Keep AccessLog only for 5xx on that path.
			if r.URL.Path == "/v1/chat/completions" && status >= 200 && status < 400 {
				return
			}
			if status >= 500 || sample >= 1 || mathrand.Float64() < sample {
				attrs := []any{
					"method", r.Method,
					"path", r.URL.Path,
					"status", status,
					"latency_ms", latency.Milliseconds(),
					"bytes", rec.Bytes(),
				}
				if id, ok := reqctx.IdentityFrom(r.Context()); ok {
					attrs = append(attrs, "tenant_id", id.TenantID, "key_prefix", id.KeyPrefix)
				}
				logging.FromContext(r.Context()).Info("http request", attrs...)
			}
		})
	}
}

// CORS applies an explicit origin allowlist. CORS is disabled when the list is empty.
func CORS(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" && o != "*" {
			allowed[o] = struct{}{}
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(allowed) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			if _, ok := allowed[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// InflightLimit rejects requests with 503 when the configured concurrency limit
// is reached. A non-positive limit disables the check.
func InflightLimit(limit int) func(http.Handler) http.Handler {
	if limit <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	sem := make(chan struct{}, limit)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				next.ServeHTTP(w, r)
			default:
				apierr.Unavailable(w, r, "server_busy", "gateway is at capacity")
			}
		})
	}
}
