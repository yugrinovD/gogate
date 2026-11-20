package ollama

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yugrinovD/gogate/internal/provider"
)

func TestChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"model":"qwen2.5:3b","message":{"role":"assistant","content":"Hi "},"done":false}`+"\n")
		_, _ = io.WriteString(w, `{"model":"qwen2.5:3b","message":{"role":"assistant","content":"there"},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":2}`+"\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "ollama", BaseURL: srv.URL, Models: []string{"qwen2.5:3b"}}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Chat(context.Background(), provider.Request{Model: "qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var text string
	var usage *provider.Usage
	var firstID string
	for {
		c, err := s.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if firstID == "" {
			firstID = c.ID
		} else if c.ID != firstID {
			t.Fatalf("stream id changed: %q -> %q", firstID, c.ID)
		}
		text += c.Content
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if text != "Hi there" {
		t.Fatalf("text=%q", text)
	}
	if usage == nil || usage.PromptTokens != 3 || usage.CompletionTokens != 2 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer srv.Close()
	p, _ := New(provider.Config{Name: "ollama", BaseURL: srv.URL, Models: []string{"qwen2.5:3b"}}, srv.Client())
	_, err := p.Chat(context.Background(), provider.Request{Model: "qwen2.5:3b"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *provider.UpstreamError
	if !errors.As(err, &ue) || !ue.Retryable() {
		t.Fatalf("expected retryable upstream error, got %v", err)
	}
}
