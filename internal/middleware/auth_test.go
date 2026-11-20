package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yugrinovD/gogate/internal/control"
	"github.com/yugrinovD/gogate/internal/security"
)

func testSnapshot(t *testing.T, key security.KeyMaterial, enabled bool, expires *time.Time) *control.Snapshot {
	t.Helper()
	return &control.Snapshot{KeysByPrefix: map[string]control.Key{
		key.Prefix: {
			ID: "key", TenantID: "tenant", Prefix: key.Prefix,
			Hash: key.Hash, Enabled: enabled, ExpiresAt: expires,
		},
	}}
}

func TestAPIKeyAuth(t *testing.T) {
	key, err := security.GenerateKey("test", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	cases := []struct {
		name    string
		token   string
		enabled bool
		expires *time.Time
		want    int
	}{
		{"valid", key.Plaintext, true, nil, http.StatusNoContent},
		{"missing", "", true, nil, http.StatusUnauthorized},
		{"invalid", "gk_test_deadbeef_deadbeef", true, nil, http.StatusUnauthorized},
		{"disabled", key.Plaintext, false, nil, http.StatusUnauthorized},
		{"expired", key.Plaintext, true, ptrTime(time.Now().Add(-time.Hour)), http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := testSnapshot(t, key, tc.enabled, tc.expires)
			h := APIKey(func() *control.Snapshot { return snap }, "0123456789abcdef", 256, nil)(next)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d", rec.Code, tc.want)
			}
		})
	}
}

func TestBodyLimit(t *testing.T) {
	h := BodyLimit(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.ContentLength = 10
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", rec.Code)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
