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
