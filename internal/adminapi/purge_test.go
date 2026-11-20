package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPurgeExactDisabled(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/cache/purge", strings.NewReader(`{"exact":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.purgeCache(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "exact_cache_disabled") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}
