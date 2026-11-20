// Package tokenest provides a cheap token estimate used for quotas, budgets and
// providers that do not report usage. It is intentionally approximate.
package tokenest

import (
	"strings"

	"github.com/yugrinovD/gogate/internal/provider"
)

// Text estimates tokens as ceil(runes/4), with a minimum of one.
func Text(s string) int {
	n := len([]rune(s))/4 + 1
	if n < 1 {
		return 1
	}
	return n
}

// Prompt estimates prompt tokens across messages.
func Prompt(messages []provider.Message) int {
	total := 0
	for _, m := range messages {
		total += Text(m.Role) + Text(m.Content) + 4
	}
	if total < 1 {
		return 1
	}
	return total
}

// Response estimates completion tokens for a text response.
func Response(s string) int {
	return Text(strings.TrimSpace(s))
}
