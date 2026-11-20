package cache

import (
	"context"
	"testing"

	"github.com/yugrinovD/gogate/internal/provider"
)

func BenchmarkKey(b *testing.B) {
	temp := 0.2
	in := KeyInput{
		TenantID: "tenant", Model: "mock",
		Messages: []provider.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello, how are you today?"},
		},
		Temperature: &temp,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Key(in)
	}
}

func BenchmarkMockEmbedder(b *testing.B) {
	e := NewMockEmbedder("mock-embed")
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = e.Embed(ctx, "hello world this is a semantic cache prompt")
	}
}
