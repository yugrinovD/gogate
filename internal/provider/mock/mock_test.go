package mock

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/yugrinovD/gogate/internal/provider"
)

func TestMockStream(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"chunk_delay_ms": 0})
	p, err := New(provider.Config{Name: "mock", Models: []string{"mock"}, RawConfig: raw})
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Chat(context.Background(), provider.Request{
		Model: "mock",
		Messages: []provider.Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var content string
	var usage *provider.Usage
	for {
		chunk, err := s.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content += chunk.Content
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if content == "" {
		t.Fatal("expected content")
	}
	if usage == nil || usage.TotalTokens == 0 {
		t.Fatal("expected usage")
	}
}

func TestMockFailBeforeFirst(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"fail_before_first": true})
	p, _ := New(provider.Config{Name: "mock", Models: []string{"mock"}, RawConfig: raw})
	_, err := p.Chat(context.Background(), provider.Request{Model: "mock"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *provider.UpstreamError
	if !errors.As(err, &ue) || !ue.Retryable() {
		t.Fatalf("expected retryable upstream error, got %v", err)
	}
}

func TestMockFailAfterChunks(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"chunk_delay_ms": 0, "fail_after_chunks": 1})
	p, _ := New(provider.Config{Name: "mock", Models: []string{"mock"}, RawConfig: raw})
	s, err := p.Chat(context.Background(), provider.Request{
		Model:    "mock",
		Messages: []provider.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Next(context.Background()); err != nil {
		t.Fatal("first chunk should succeed")
	}
	if _, err := s.Next(context.Background()); err == nil {
		t.Fatal("expected failure after configured chunks")
	}
}
