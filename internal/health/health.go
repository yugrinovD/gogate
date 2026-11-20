// Package health exposes liveness and readiness endpoints.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/yugrinovD/gogate/internal/store"
	"github.com/yugrinovD/gogate/internal/version"
)

// Handler serves /healthz and /readyz.
type Handler struct {
	store         *store.Store
	redis         *redis.Client
	redisRequired bool
}

// New creates a health handler. redisRequired controls whether a Redis outage
// makes the process not ready (fail-closed rate limiting).
func New(st *store.Store, rdb *redis.Client, redisRequired bool) *Handler {
	return &Handler{store: st, redis: rdb, redisRequired: redisRequired}
}

// Live handles GET /healthz.
func (h *Handler) Live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version.String()})
}

// Ready handles GET /readyz and checks PostgreSQL and Redis.
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	components := map[string]any{}
	ready := true

	if err := h.store.Ping(ctx); err != nil {
		components["postgres"] = map[string]any{"ok": false}
		ready = false
	} else {
		components["postgres"] = map[string]any{"ok": true}
	}
	if h.redis != nil {
		err := h.redis.Ping(ctx).Err()
		components["redis"] = map[string]any{"ok": err == nil, "required": h.redisRequired}
		if err != nil && h.redisRequired {
			ready = false
		}
	} else {
		components["redis"] = map[string]any{"ok": false, "required": false}
	}
	status := http.StatusOK
	state := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}
	body := map[string]any{"status": state, "version": version.String()}
	if r.URL.Query().Get("verbose") == "1" || !ready {
		body["components"] = components
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
