package usage

import (
	"testing"
	"time"

	"github.com/yugrinovD/gogate/internal/store"
)

func TestMergeUsage(t *testing.T) {
	day := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	in := []store.UsageRecord{
		{TenantID: "t", APIKeyID: "k", Provider: "p", Model: "m", Day: day, Requests: 1, PromptTokens: 2, CostUSD: 0.1},
		{TenantID: "t", APIKeyID: "k", Provider: "p", Model: "m", Day: day.Add(time.Hour), Requests: 2, PromptTokens: 3, CostUSD: 0.2},
		{TenantID: "t", APIKeyID: "k", Provider: "p", Model: "other", Day: day, Requests: 1},
	}
	out := mergeUsage(in)
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0].Requests != 3 || out[0].PromptTokens != 5 {
		t.Fatalf("merged=%+v", out[0])
	}
	if out[0].CostUSD < 0.299 || out[0].CostUSD > 0.301 {
		t.Fatalf("cost=%v", out[0].CostUSD)
	}
}
