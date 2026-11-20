package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/yugrinovD/gogate/internal/logging"
	"github.com/yugrinovD/gogate/internal/reqctx"
)

func TestFinishLogIncludesRequestID(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	ctx := reqctx.WithRequestID(context.Background(), "req-test-1")
	ctx = logging.WithLogger(ctx, base.With("request_id", "req-test-1"))
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	h := &Handler{log: base}
	h.finish(req, outcome{Model: "mock", Status: 200})
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode log: %v (%s)", err, buf.String())
	}
	if line["request_id"] != "req-test-1" {
		t.Fatalf("request_id=%v log=%s", line["request_id"], buf.String())
	}
}
