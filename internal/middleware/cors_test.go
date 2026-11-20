package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	t.Run("allowed preflight", func(t *testing.T) {
		h := CORS([]string{"https://example.com"})(next)
		req := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
		req.Header.Set("Origin", "https://example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status=%d", rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
			t.Fatal("missing allow-origin")
		}
	})

	t.Run("disallowed origin", func(t *testing.T) {
		h := CORS([]string{"https://example.com"})(next)
		req := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
		req.Header.Set("Origin", "https://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("unexpected allow-origin")
		}
	})

	t.Run("disabled passes through", func(t *testing.T) {
		h := CORS(nil)(next)
		req := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTeapot {
			t.Fatalf("status=%d", rec.Code)
		}
	})
}
