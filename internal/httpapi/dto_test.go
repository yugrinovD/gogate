package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/yugrinovD/gogate/internal/config"
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
