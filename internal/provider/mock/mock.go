// Package mock provides a deterministic local provider for demos, tests and
// load generation.
package mock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"strings"
	"time"

	"github.com/yugrinovD/gogate/internal/provider"
)

// Provider implements provider.Provider with synthetic responses.
type Provider struct {
	name   string
	models []string
	cfg    config
}

type config struct {
	LatencyMS       int     `json:"latency_ms"`
	ChunkDelayMS    int     `json:"chunk_delay_ms"`
	FailBeforeFirst bool    `json:"fail_before_first"`
	FailAfterChunks int     `json:"fail_after_chunks"`
	FailRate        float64 `json:"fail_rate"`
	ErrorMessage    string  `json:"error_message"`
}

// New creates a mock provider from a registry config.
func New(c provider.Config) (*Provider, error) {
	cfg := config{}
	if len(c.RawConfig) > 0 {
		if err := json.Unmarshal(c.RawConfig, &cfg); err != nil {
			return nil, fmt.Errorf("parse mock config: %w", err)
		}
	}
	if cfg.ErrorMessage == "" {
		cfg.ErrorMessage = "mock upstream failure"
	}
	return &Provider{name: c.Name, models: append([]string(nil), c.Models...), cfg: cfg}, nil
}

func (p *Provider) Name() string     { return p.name }
func (p *Provider) Kind() string     { return "mock" }
func (p *Provider) Models() []string { return append([]string(nil), p.models...) }

func (p *Provider) Ping(context.Context) error { return nil }

func (p *Provider) Chat(ctx context.Context, req provider.Request) (provider.Stream, error) {
	if p.cfg.FailBeforeFirst || (p.cfg.FailRate > 0 && mathrand.Float64() < p.cfg.FailRate) {
		return nil, &provider.UpstreamError{Provider: p.name, Message: p.cfg.ErrorMessage}
	}
	content := buildResponse(req)
	chunks := splitWords(content)
	usage := &provider.Usage{
		PromptTokens:     estimateTokens(messagesText(req.Messages)),
		CompletionTokens: estimateTokens(content),
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return &stream{
		provider: p,
		ctx:      ctx,
		id:       "chatcmpl-mock-" + randomID(),
		created:  time.Now().Unix(),
		model:    req.Model,
		chunks:   chunks,
		usage:    usage,
		delay:    time.Duration(p.cfg.ChunkDelayMS) * time.Millisecond,
		latency:  time.Duration(p.cfg.LatencyMS) * time.Millisecond,
	}, nil
}

type stream struct {
	provider *Provider
	ctx      context.Context
	id       string
	created  int64
	model    string
	chunks   []string
	usage    *provider.Usage
	delay    time.Duration
	latency  time.Duration
	index    int
	closed   bool
	failed   bool
	first    bool
}

func (s *stream) Next(ctx context.Context) (provider.Chunk, error) {
	if s.closed {
		return provider.Chunk{}, io.EOF
	}
	if ctx == nil {
		ctx = s.ctx
	}
	if !s.first {
		s.first = true
		if s.latency > 0 {
			if err := sleep(ctx, s.latency); err != nil {
				return provider.Chunk{}, err
			}
		}
	}
	if s.index >= len(s.chunks) {
		s.closed = true
		return provider.Chunk{}, io.EOF
	}
	if s.provider.cfg.FailAfterChunks > 0 && s.index >= s.provider.cfg.FailAfterChunks && !s.failed {
		s.failed = true
		s.closed = true
		return provider.Chunk{}, &provider.UpstreamError{Provider: s.provider.name, Message: s.provider.cfg.ErrorMessage}
	}
	if s.delay > 0 {
		if err := sleep(ctx, s.delay); err != nil {
			return provider.Chunk{}, err
		}
	}
	chunk := provider.Chunk{
		ID:      s.id,
		Created: s.created,
		Model:   s.model,
		Index:   0,
		Content: s.chunks[s.index],
	}
	if s.index == 0 {
		chunk.Role = "assistant"
	}
	s.index++
	if s.index >= len(s.chunks) {
		chunk.FinishReason = "stop"
		chunk.Usage = s.usage
		s.closed = true
	}
	return chunk, nil
}

func (s *stream) Close() error { s.closed = true; return nil }

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func buildResponse(req provider.Request) string {
	last := "hello"
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" && strings.TrimSpace(req.Messages[i].Content) != "" {
			last = strings.TrimSpace(req.Messages[i].Content)
			break
		}
	}
	if len(last) > 240 {
		last = last[:240]
	}
	return "This is a mock response from gogate. You said: " + last
}

func splitWords(s string) []string {
	var out []string
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
		if r == ' ' {
			out = append(out, b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	if len(out) == 0 {
		out = []string{" "}
	}
	return out
}

func estimateTokens(s string) int {
	n := len([]rune(s))/4 + 1
	if n < 1 {
		return 1
	}
	return n
}

func messagesText(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func randomID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
