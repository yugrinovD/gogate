package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yugrinovD/gogate/internal/provider"
)

func TestChatSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization=%q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":2,\"total_tokens\":4}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "openai", BaseURL: srv.URL + "/v1", APIKey: "secret", Models: []string{"m"}}, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Chat(context.Background(), provider.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var text string
	var usage *provider.Usage
	for {
		c, err := s.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		text += c.Content
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if text != "hello world" {
		t.Fatalf("text=%q", text)
	}
	if usage == nil || usage.TotalTokens != 4 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestChatErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down","code":"rate_limit"}}`)
	}))
	defer srv.Close()
	p, _ := New(provider.Config{Name: "openai", BaseURL: srv.URL + "/v1", Models: []string{"m"}}, srv.Client(), nil)
	_, err := p.Chat(context.Background(), provider.Request{Model: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *provider.UpstreamError
	if !errors.As(err, &ue) || ue.Message != "slow down" || !ue.Retryable() {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestStreamOptionsFallback(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		raw, _ := io.ReadAll(r.Body)
		if requests == 1 {
			if !bytes.Contains(raw, []byte("stream_options")) {
				t.Fatal("first request should include stream_options")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"stream_options is not supported"}}`)
			return
		}
		if bytes.Contains(raw, []byte("stream_options")) {
			t.Fatal("fallback request must not include stream_options")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	p, err := New(provider.Config{Name: "openai", BaseURL: srv.URL + "/v1", Models: []string{"m"}}, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Chat(context.Background(), provider.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := provider.ParseRetryAfter("3"); got != 3*time.Second {
		t.Fatalf("got %s", got)
	}
	if got := provider.ParseRetryAfter(""); got != 0 {
		t.Fatalf("got %s", got)
	}
}
