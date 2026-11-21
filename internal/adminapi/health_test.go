package adminapi

import (
	"context"
	"testing"
)

func TestPingRedisNil(t *testing.T) {
	h := &Handler{}
	got := h.pingRedis(context.Background())
	if got["ok"] != false {
		t.Fatalf("ok=%v", got["ok"])
	}
	if got["error"] != "redis is not configured" {
		t.Fatalf("error=%v", got["error"])
	}
}

func TestHealthCodeHonoursFailOpen(t *testing.T) {
	if got := healthCode(true, false, true); got != 503 {
		t.Fatalf("redis required: status=%d", got)
	}
	if got := healthCode(true, false, false); got != 200 {
		t.Fatalf("fail-open: status=%d", got)
	}
	if got := healthCode(false, true, true); got != 503 {
		t.Fatalf("database down: status=%d", got)
	}
}
