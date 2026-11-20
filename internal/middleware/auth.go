package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/yugrinovD/gogate/internal/apierr"
	"github.com/yugrinovD/gogate/internal/control"
	"github.com/yugrinovD/gogate/internal/reqctx"
	"github.com/yugrinovD/gogate/internal/security"
)

// SnapshotProvider returns the current control-plane snapshot.
type SnapshotProvider func() *control.Snapshot

// APIKey authenticates public API requests using a hashed key.
func APIKey(snapshot SnapshotProvider, pepper string, maxKeyBytes int, onAuth func(reqctx.Identity)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				w.Header().Set("WWW-Authenticate", "Bearer")
				apierr.Unauthorized(w, r, "invalid_api_key", "missing API key")
				return
			}
			prefix, secret, err := security.ParseKeyLimited(token, maxKeyBytes)
			if err != nil {
				apierr.Unauthorized(w, r, "invalid_api_key", "invalid API key")
				return
			}
			snap := snapshot()
			if snap == nil {
				apierr.Unavailable(w, r, "not_ready", "gateway is not ready")
				return
			}
			k, ok := snap.KeysByPrefix[prefix]
			if !ok || !k.Enabled {
				apierr.Unauthorized(w, r, "invalid_api_key", "invalid API key")
				return
			}
			if k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now()) {
				apierr.Unauthorized(w, r, "invalid_api_key", "API key expired")
				return
			}
			if !security.Verify(k.Hash, security.HashSecret(pepper, secret)) {
				apierr.Unauthorized(w, r, "invalid_api_key", "invalid API key")
				return
			}
			id := reqctx.Identity{KeyID: k.ID, TenantID: k.TenantID, KeyPrefix: k.Prefix, KeyName: k.Name}
			r = r.WithContext(reqctx.WithIdentity(r.Context(), id))
			if onAuth != nil {
				onAuth(id)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// AdminToken authenticates admin API requests with a constant-time comparison.
func AdminToken(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			given := bearerToken(r)
			if len(given) == 0 || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				apierr.Unauthorized(w, r, "invalid_admin_token", "invalid admin token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
