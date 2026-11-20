package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yugrinovD/gogate/internal/middleware"
)

func TestPublicRouting(t *testing.T) {
	var apiCalls, systemCalls int
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		w.WriteHeader(http.StatusOK)
	})
	system := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		systemCalls++
		w.WriteHeader(http.StatusOK)
	})
	h := newPublicHandler(true, system, api)

	cases := []struct {
		method string
		path   string
		status int
		allow  string
	}{
		{http.MethodPost, "/v1/chat/completions", http.StatusOK, ""},
		{http.MethodGet, "/v1/models", http.StatusOK, ""},
		{http.MethodGet, "/healthz", http.StatusOK, ""},
		{http.MethodGet, "/metrics", http.StatusOK, ""},
		{http.MethodPatch, "/v1/models", http.StatusMethodNotAllowed, "GET"},
		{http.MethodGet, "/unknown", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.allow != "" && rec.Header().Get("Allow") != tc.allow {
				t.Fatalf("allow=%q", rec.Header().Get("Allow"))
			}
			if tc.status == http.StatusNotFound || tc.status == http.StatusMethodNotAllowed {
				var env map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env["error"] == nil {
					t.Fatalf("expected JSON error, body=%s", rec.Body.String())
				}
			}
		})
	}
	if apiCalls != 2 {
		t.Fatalf("api calls=%d", apiCalls)
	}
	if systemCalls != 2 {
		t.Fatalf("system calls=%d", systemCalls)
	}
}

func TestCORSPreflightRunsBeforeRouting(t *testing.T) {
	called := false
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	system := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := middleware.CORS([]string{"https://example.com"})(newPublicHandler(true, system, api))

	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Fatal("missing Access-Control-Allow-Origin")
	}
	if called {
		t.Fatal("preflight must not reach API handler")
	}
}

func TestRequestIDOnNotFound(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	system := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := middleware.RequestID(slog.New(slog.NewTextHandler(io.Discard, nil)))(newPublicHandler(true, system, api))
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
	if strings.TrimSpace(rec.Header().Get("X-Request-ID")) == "" {
		t.Fatal("missing X-Request-ID")
	}
}
