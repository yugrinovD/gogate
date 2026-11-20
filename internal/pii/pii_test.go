package pii

import (
	"strings"
	"testing"

	"github.com/yugrinovD/gogate/internal/provider"
)

func TestRedactText(t *testing.T) {
	in := "mail me at alice@example.com or +1 (555) 123-4567, card 4111 1111 1111 1111"
	out, n := RedactText(in)
	if n < 3 {
		t.Fatalf("replacements=%d out=%q", n, out)
	}
	for _, bad := range []string{"alice@example.com", "555", "4111 1111"} {
		if strings.Contains(out, bad) {
			t.Fatalf("still contains %q: %s", bad, out)
		}
	}
}

func TestRedactMessages(t *testing.T) {
	in := []provider.Message{{Role: "user", Content: "email: bob@example.org"}}
	out, changed := RedactMessages(in)
	if !changed {
		t.Fatal("expected change")
	}
	if in[0].Content == out[0].Content {
		t.Fatal("input should not be mutated")
	}
	if !strings.Contains(out[0].Content, "[REDACTED_EMAIL]") {
		t.Fatalf("out=%q", out[0].Content)
	}
}

func TestLuhnRejectsInvalidCard(t *testing.T) {
	out, _ := RedactText("number 1234 5678 9012 3456")
	if strings.Contains(out, "[REDACTED_CARD]") {
		t.Fatal("invalid Luhn number should not be redacted as a card")
	}
}
