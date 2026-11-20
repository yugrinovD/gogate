package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/yugrinovD/gogate/internal/config"
	"github.com/yugrinovD/gogate/internal/orchestrator"
	"github.com/yugrinovD/gogate/internal/provider"
)

func TestParseContent(t *testing.T) {
	s, err := parseContent(json.RawMessage(`"hello"`))
	if err != nil || s != "hello" {
		t.Fatalf("s=%q err=%v", s, err)
	}
	s, err = parseContent(json.RawMessage(`[{"type":"text","text":"a"},{"type":"input_text","text":"b"}]`))
	if err != nil || s != "ab" {
		t.Fatalf("s=%q err=%v", s, err)
	}
	if _, err := parseContent(json.RawMessage(`[{"type":"image_url","image_url":{}}]`)); err == nil {
		t.Fatal("expected image content rejection")
	}
}

func TestParseStop(t *testing.T) {
	stops, err := parseStop(json.RawMessage(`"END"`))
	if err != nil || len(stops) != 1 || stops[0] != "END" {
		t.Fatalf("stops=%v err=%v", stops, err)
	}
	stops, err = parseStop(json.RawMessage(`["a","b"]`))
	if err != nil || len(stops) != 2 {
		t.Fatalf("stops=%v err=%v", stops, err)
	}
	if _, err := parseStop(json.RawMessage(`["1","2","3","4","5"]`)); err == nil {
		t.Fatal("expected too many stops")
	}
}

func TestValidateBounds(t *testing.T) {
	h := &Handler{cfg: config.Config{MaxOutputTokens: 16, MaxMessages: 2}}
	tooHot := 3.0
	req := chatRequest{Model: "mock", Messages: []messageIn{{Role: "user", Content: json.RawMessage(`"hi"`)}}, Temperature: &tooHot}
	if code, _ := h.validate(&req); code != "invalid_temperature" {
		t.Fatalf("code=%s", code)
	}
	tooLong := 17
	req = chatRequest{Model: "mock", Messages: []messageIn{{Role: "user", Content: json.RawMessage(`"hi"`)}}, MaxTokens: &tooLong}
	if code, _ := h.validate(&req); code != "invalid_max_tokens" {
		t.Fatalf("code=%s", code)
	}
	req = chatRequest{
		Model: "mock",
		Messages: []messageIn{
			{Role: "user", Content: json.RawMessage(`"a"`)},
			{Role: "user", Content: json.RawMessage(`"b"`)},
			{Role: "user", Content: json.RawMessage(`"c"`)},
		},
	}
	if code, _ := h.validate(&req); code != "too_many_messages" {
		t.Fatalf("code=%s", code)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteSSEReturnsWriteError(t *testing.T) {
	if err := writeSSE(failingWriter{}, chatChunk{}); err == nil {
		t.Fatal("expected write error")
	}
}

func TestMapOpenError(t *testing.T) {
	status, code, _ := mapOpenError(orchestrator.ErrFirstByteTimeout)
	if status != http.StatusGatewayTimeout || code != "upstream_timeout" {
		t.Fatalf("timeout mapping: %d %s", status, code)
	}
	status, code, _ = mapOpenError(&provider.UpstreamError{StatusCode: http.StatusBadRequest})
	if status != http.StatusBadRequest || code != "upstream_invalid_request" {
		t.Fatalf("400 mapping: %d %s", status, code)
	}
	status, code, _ = mapOpenError(&provider.UpstreamError{StatusCode: http.StatusTooManyRequests})
	if status != http.StatusTooManyRequests || code != "rate_limit_exceeded" {
		t.Fatalf("429 mapping: %d %s", status, code)
	}
	status, _, _ = mapOpenError(&provider.UpstreamError{StatusCode: http.StatusInternalServerError})
	if status != http.StatusBadGateway {
		t.Fatalf("500 mapping: %d", status)
	}
}

func FuzzParseContent(f *testing.F) {
	f.Add([]byte(`"hello"`))
	f.Add([]byte(`[{"type":"text","text":"hi"}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseContent(json.RawMessage(data))
	})
}

func FuzzParseStop(f *testing.F) {
	f.Add([]byte(`"END"`))
	f.Add([]byte(`["a","b"]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseStop(json.RawMessage(data))
	})
}
