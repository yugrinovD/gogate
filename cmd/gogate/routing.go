package main

import (
	"net/http"

	"github.com/yugrinovD/gogate/internal/apierr"
)

// newPublicHandler dispatches system and API routes. It deliberately performs
// method checks for API paths so unknown methods return the OpenAI JSON error
// envelope instead of the plain-text 405 from http.ServeMux.
func newPublicHandler(metricsEnabled bool, system, api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/chat/completions":
			if r.Method != http.MethodPost {
				methodNotAllowed(w, r, "POST")
				return
			}
			api.ServeHTTP(w, r)
		case r.URL.Path == "/v1/models":
			if r.Method != http.MethodGet {
				methodNotAllowed(w, r, "GET")
				return
			}
			api.ServeHTTP(w, r)
		case r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || (metricsEnabled && r.URL.Path == "/metrics"):
			if r.Method != http.MethodGet {
				methodNotAllowed(w, r, "GET")
				return
			}
			system.ServeHTTP(w, r)
		default:
			apierr.NotFound(w, r, "not_found", "endpoint not found")
		}
	})
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	apierr.Write(w, r, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
}
