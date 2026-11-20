// Package provider defines the chat provider abstraction and implementations.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Message is a single chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is the provider-neutral chat request.
type Request struct {
	Model            string
	Messages         []Message
	Temperature      *float64
	TopP             *float64
	MaxTokens        *int
	Stop             []string
	Seed             *int
	PresencePenalty  *float64
	FrequencyPenalty *float64
	Stream           bool
}

// Usage contains token accounting returned by a provider.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Chunk is a normalized streaming chat chunk.
type Chunk struct {
	ID           string
	Created      int64
	Model        string
	Index        int
	Role         string
	Content      string
	FinishReason string
	Usage        *Usage
}

// Stream is an active provider response. Next returns io.EOF after the last
// chunk. Close is safe to call multiple times.
type Stream interface {
	Next(ctx context.Context) (Chunk, error)
	Close() error
}

// Provider is a stateless upstream adapter.
type Provider interface {
	Name() string
	Kind() string
	Models() []string
	Chat(ctx context.Context, req Request) (Stream, error)
	Ping(ctx context.Context) error
}

// Config is a provider definition loaded from the control plane.
type Config struct {
	Name       string
	Kind       string
	BaseURL    string
	APIKeyEnv  string
	APIKey     string
	Models     []string
	Priority   int
	Timeout    time.Duration
	MaxRetries int
	RawConfig  json.RawMessage
}

// ModelInfo describes a model exposed by GET /v1/models.
type ModelInfo struct {
	ID      string
	OwnedBy string
}

// ParseRetryAfter parses an HTTP Retry-After value (seconds or HTTP date).
func ParseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// UpstreamError is a normalized upstream failure.
type UpstreamError struct {
	Provider   string
	StatusCode int
	Code       string
	Message    string
	RetryAfter time.Duration
	Err        error
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return fmt.Sprintf("provider %s: %s: %v", e.Provider, e.Message, e.Err)
	}
	return fmt.Sprintf("provider %s: %s", e.Provider, e.Message)
}

func (e *UpstreamError) Unwrap() error { return e.Err }

// Retryable reports whether the error is worth retrying or falling back.
func (e *UpstreamError) Retryable() bool {
	if e == nil {
		return false
	}
	if e.Err != nil {
		switch {
		case errors.Is(e.Err, context.Canceled):
			return false
		case errors.Is(e.Err, context.DeadlineExceeded):
			return true
		}
	}
	if e.StatusCode == 0 {
		return true
	}
	if e.StatusCode == 408 || e.StatusCode == 425 || e.StatusCode == 429 {
		return true
	}
	return e.StatusCode >= 500
}
