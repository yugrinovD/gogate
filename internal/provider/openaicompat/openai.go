// Package openaicompat implements an OpenAI-compatible upstream adapter.
package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/yugrinovD/gogate/internal/provider"
)

// Provider proxies requests to an OpenAI-compatible /chat/completions endpoint.
type Provider struct {
	name                 string
	baseURL              string
	apiKey               string
	models               []string
	client               *http.Client
	log                  *slog.Logger
	disableStreamOptions bool
}

// New creates an OpenAI-compatible adapter.
func New(c provider.Config, client *http.Client, log *slog.Logger) (*Provider, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if log == nil {
		log = slog.Default()
	}
	var raw struct {
		DisableStreamOptions bool `json:"disable_stream_options"`
	}
	if len(c.RawConfig) > 0 {
		_ = json.Unmarshal(c.RawConfig, &raw)
	}
	return &Provider{
		name:                 c.Name,
		baseURL:              strings.TrimRight(c.BaseURL, "/"),
		apiKey:               c.APIKey,
		models:               append([]string(nil), c.Models...),
		client:               client,
		log:                  log,
		disableStreamOptions: raw.DisableStreamOptions,
	}, nil
}

func (p *Provider) Name() string     { return p.name }
func (p *Provider) Kind() string     { return "openai_compatible" }
func (p *Provider) Models() []string { return append([]string(nil), p.models...) }

func (p *Provider) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return err
	}
	p.authorize(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return &provider.UpstreamError{Provider: p.name, Message: "upstream health check failed", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return &provider.UpstreamError{Provider: p.name, StatusCode: resp.StatusCode, Message: "upstream health check failed"}
	}
	return nil
}

type requestBody struct {
	Model            string             `json:"model"`
	Messages         []provider.Message `json:"messages"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
	MaxTokens        *int               `json:"max_tokens,omitempty"`
	Stop             []string           `json:"stop,omitempty"`
	Seed             *int               `json:"seed,omitempty"`
	PresencePenalty  *float64           `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64           `json:"frequency_penalty,omitempty"`
	Stream           bool               `json:"stream"`
	StreamOptions    map[string]bool    `json:"stream_options,omitempty"`
}

func (p *Provider) Chat(ctx context.Context, req provider.Request) (provider.Stream, error) {
	body := requestBody{
		Model:            req.Model,
		Messages:         req.Messages,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		MaxTokens:        req.MaxTokens,
		Stop:             req.Stop,
		Seed:             req.Seed,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		Stream:           true,
	}
	if !p.disableStreamOptions {
		body.StreamOptions = map[string]bool{"include_usage": true}
	}
	return p.doChat(ctx, body, true)
}

func (p *Provider) doChat(ctx context.Context, body requestBody, allowFallback bool) (provider.Stream, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	p.authorize(httpReq)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &provider.UpstreamError{Provider: p.name, Message: "upstream request failed", Err: err}
	}
	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		_ = resp.Body.Close()
		if allowFallback && body.StreamOptions != nil && resp.StatusCode == http.StatusBadRequest &&
			bytes.Contains(bytes.ToLower(bodyBytes), []byte("stream_options")) {
			body.StreamOptions = nil
			return p.doChat(ctx, body, false)
		}
		return nil, &provider.UpstreamError{
			Provider:   p.name,
			StatusCode: resp.StatusCode,
			Message:    upstreamErrorMessage(bodyBytes),
			RetryAfter: provider.ParseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 2<<20)
	return &stream{provider: p.name, body: resp.Body, scanner: scanner, log: p.log}, nil
}

func (p *Provider) authorize(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

func upstreamErrorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 512 {
		msg = msg[:512]
	}
	if msg == "" {
		return "upstream returned an error"
	}
	return msg
}

type openAIChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

type stream struct {
	provider string
	body     io.ReadCloser
	scanner  *bufio.Scanner
	log      *slog.Logger
	closed   bool
}

func (s *stream) Next(ctx context.Context) (provider.Chunk, error) {
	if s.closed {
		return provider.Chunk{}, io.EOF
	}
	for s.scanner.Scan() {
		select {
		case <-ctx.Done():
			return provider.Chunk{}, ctx.Err()
		default:
		}
		line := strings.TrimSpace(s.scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			s.closed = true
			_ = s.body.Close()
			return provider.Chunk{}, io.EOF
		}
		var c openAIChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return provider.Chunk{}, &provider.UpstreamError{Provider: s.provider, Message: "invalid upstream SSE chunk", Err: err}
		}
		if c.Error != nil {
			return provider.Chunk{}, &provider.UpstreamError{Provider: s.provider, Code: c.Error.Code, Message: c.Error.Message}
		}
		chunk := provider.Chunk{ID: c.ID, Created: c.Created, Model: c.Model}
		if len(c.Choices) > 0 {
			ch := c.Choices[0]
			chunk.Index = ch.Index
			chunk.Role = ch.Delta.Role
			chunk.Content = ch.Delta.Content
			if ch.FinishReason != nil {
				chunk.FinishReason = *ch.FinishReason
			}
		}
		if c.Usage != nil {
			chunk.Usage = &provider.Usage{
				PromptTokens:     c.Usage.PromptTokens,
				CompletionTokens: c.Usage.CompletionTokens,
				TotalTokens:      c.Usage.TotalTokens,
			}
		}
		if chunk.Content == "" && chunk.Usage == nil && chunk.FinishReason == "" {
			continue
		}
		return chunk, nil
	}
	if err := s.scanner.Err(); err != nil {
		return provider.Chunk{}, &provider.UpstreamError{Provider: s.provider, Message: "upstream SSE read error", Err: err}
	}
	s.closed = true
	_ = s.body.Close()
	return provider.Chunk{}, io.EOF
}

func (s *stream) Close() error {
	s.closed = true
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
