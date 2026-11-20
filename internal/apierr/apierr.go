// Package apierr writes OpenAI-compatible error envelopes.
package apierr

import (
	"encoding/json"
	"net/http"
)

// Error is the OpenAI error object.
type Error struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

// Envelope is the top-level OpenAI error response.
type Envelope struct {
	Error Error `json:"error"`
}

// Write writes a JSON error with the OpenAI envelope.
func Write(w http.ResponseWriter, r *http.Request, status int, typ, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Envelope{Error: Error{
		Message: message,
		Type:    typ,
		Code:    code,
		Param:   "",
	}})
}

// BadRequest writes a 400 error.
func BadRequest(w http.ResponseWriter, r *http.Request, code, message string) {
	Write(w, r, http.StatusBadRequest, "invalid_request_error", code, message)
}

// Unauthorized writes a 401 error.
func Unauthorized(w http.ResponseWriter, r *http.Request, code, message string) {
	Write(w, r, http.StatusUnauthorized, "authentication_error", code, message)
}

// NotFound writes a 404 error.
func NotFound(w http.ResponseWriter, r *http.Request, code, message string) {
	Write(w, r, http.StatusNotFound, "invalid_request_error", code, message)
}

// RateLimited writes a 429 error.
func RateLimited(w http.ResponseWriter, r *http.Request, code, message string, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	Write(w, r, http.StatusTooManyRequests, "rate_limit_error", code, message)
}

// Internal writes a 500 error.
func Internal(w http.ResponseWriter, r *http.Request, message string) {
	Write(w, r, http.StatusInternalServerError, "server_error", "internal_error", message)
}

// Upstream writes a 502 error.
func Upstream(w http.ResponseWriter, r *http.Request, code, message string) {
	Write(w, r, http.StatusBadGateway, "upstream_error", code, message)
}

// Unavailable writes a 503 error.
func Unavailable(w http.ResponseWriter, r *http.Request, code, message string) {
	Write(w, r, http.StatusServiceUnavailable, "server_error", code, message)
}

// Timeout writes a 504 error.
func Timeout(w http.ResponseWriter, r *http.Request, message string) {
	Write(w, r, http.StatusGatewayTimeout, "upstream_error", "upstream_timeout", message)
}
