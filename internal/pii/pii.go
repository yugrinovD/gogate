// Package pii provides conservative regex-based redaction for prompts.
package pii

import (
	"regexp"
	"strings"

	"github.com/yugrinovD/gogate/internal/provider"
)

var (
	emailRe = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	phoneRe = regexp.MustCompile(`(?:\+?\d[\d\s().\-]{7,}\d)`)
	cardRe  = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
)

// RedactText replaces likely PII and returns the number of replacements.
func RedactText(s string) (string, int) {
	count := 0
	s, n := replaceMatches(s, emailRe, "[REDACTED_EMAIL]", nil)
	count += n
	s, n = replaceMatches(s, cardRe, "[REDACTED_CARD]", luhnMatch)
	count += n
	s, n = replaceMatches(s, phoneRe, "[REDACTED_PHONE]", nil)
	count += n
	return s, count
}

// RedactMessages redacts all message content in place and reports whether
// anything changed.
func RedactMessages(messages []provider.Message) ([]provider.Message, bool) {
	changed := false
	out := make([]provider.Message, len(messages))
	copy(out, messages)
	for i := range out {
		redacted, n := RedactText(out[i].Content)
		if n > 0 {
			out[i].Content = redacted
			changed = true
		}
	}
	return out, changed
}

func replaceMatches(s string, re *regexp.Regexp, replacement string, validate func(string) bool) (string, int) {
	count := 0
	out := re.ReplaceAllStringFunc(s, func(m string) string {
		if validate != nil && !validate(m) {
			return m
		}
		count++
		return replacement
	})
	return out, count
}

func luhnMatch(m string) bool {
	var digits []int
	for _, r := range m {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	alt := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

// LooksLikePII is a cheap heuristic used by tests and diagnostics.
func LooksLikePII(s string) bool {
	return strings.Contains(s, "@") || phoneRe.MatchString(s) || cardRe.MatchString(s)
}
