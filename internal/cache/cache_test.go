package cache

import (
	"context"
	"testing"

	"github.com/yugrinovD/gogate/internal/provider"
)

func TestKeyIsTenantScopedAndStable(t *testing.T) {
	temp := 0.2
	in := KeyInput{
		TenantID: "t1", Model: "mock",
		Messages:    []provider.Message{{Role: "user", Content: "hello"}},
		Temperature: &temp,
	}
	k1, h1, p1 := Key(in)
	k2, h2, _ := Key(in)
	if k1 != k2 || string(h1) != string(h2) {
		t.Fatal("cache key must be stable")
	}
	if p1 == "" {
		t.Fatal("canonical prompt is empty")
	}
	in.TenantID = "t2"
	k3, _, _ := Key(in)
	if k3 == k1 {
		t.Fatal("cache key must be tenant scoped")
	}
	in.Model = "mock-fast"
	k4, _, _ := Key(in)
	if k4 == k3 {
		t.Fatal("cache key must include model")
	}
}

func TestMockEmbedderSimilarity(t *testing.T) {
	e := NewMockEmbedder("")
	a, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := e.Embed(context.Background(), "hello world")
	if len(a) != 768 {
		t.Fatalf("dimensions=%d", len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("mock embedder must be deterministic")
		}
	}
}

func TestParamsHashIncludesGenerationParameters(t *testing.T) {
	temp := 0.2
	base := KeyInput{Model: "mock", Temperature: &temp}
	h1 := ParamsHash(base)
	same := ParamsHash(base)
	if string(h1) != string(same) {
		t.Fatal("params hash must be stable")
	}
	otherTemp := 0.7
	base.Temperature = &otherTemp
	h2 := ParamsHash(base)
	if string(h1) == string(h2) {
		t.Fatal("temperature must change params hash")
	}
	base.Temperature = &temp
	maxTokens := 64
	base.MaxTokens = &maxTokens
	h3 := ParamsHash(base)
	if string(h1) == string(h3) {
		t.Fatal("max_tokens must change params hash")
	}
	base.PIIRedaction = true
	h4 := ParamsHash(base)
	if string(h3) == string(h4) {
		t.Fatal("PII mode must change params hash")
	}
}
