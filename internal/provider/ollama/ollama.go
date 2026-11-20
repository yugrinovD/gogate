// Package ollama implements the native Ollama chat API adapter.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yugrinovD/gogate/internal/provider"
)

// Provider talks to Ollama's /api/chat endpoint.
type Provider struct {
	name    string
	baseURL string
	models  []string
	client  *http.Client
}

// New creates an Ollama adapter.
func New(c provider.Config, client *http.Client) (*Provider, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{
		name:    c.Name,
		baseURL: strings.TrimRight(c.BaseURL, "/"),
		models:  append([]string(nil), c.Models...),
		client:  client,
	}, nil
}

func (p *Provider) Name() string     { return p.name }
func (p *Provider) Kind() string     { return "ollama" }
func (p *Provider) Models() []string { return append([]string(nil), p.models...) }

func (p *Provider) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return &provider.UpstreamError{Provider: p.name, StatusCode: resp.StatusCode, Message: "ollama health check failed"}
	}
	return nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []chatMessage  `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type chatResponse struct {
	Model           string      `json:"model"`
	CreatedAt       time.Time   `json:"created_at"`
	Message         chatMessage `json:"message"`
	Done            bool        `json:"done"`
	DoneReason      string      `json:"done_reason"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
	Error           string      `json:"error"`
}

func (p *Provider) Chat(ctx context.Context, req provider.Request) (provider.Stream, error) {
	body := chatRequest{Model: req.Model, Stream: true, Options: map[string]any{}}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, chatMessage{Role: m.Role, Content: m.Content})
	}
	if req.Temperature != nil {
		body.Options["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body.Options["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		body.Options["num_predict"] = *req.MaxTokens
	}
	if req.Seed != nil {
		body.Options["seed"] = *req.Seed
	}
	if len(req.Stop) > 0 {
		body.Options["stop"] = req.Stop
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &provider.UpstreamError{Provider: p.name, Message: "ollama request failed", Err: err}
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		_ = resp.Body.Close()
		return nil, &provider.UpstreamError{Provider: p.name, StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	return &stream{provider: p.name, body: resp.Body, scanner: scanner}, nil
}

type stream struct {
	provider string
	body     io.ReadCloser
	scanner  *bufio.Scanner
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
		line := bytes.TrimSpace(s.scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var r chatResponse
		if err := json.Unmarshal(line, &r); err != nil {
			return provider.Chunk{}, &provider.UpstreamError{Provider: s.provider, Message: "invalid ollama stream chunk", Err: err}
		}
		if r.Error != "" {
			return provider.Chunk{}, &provider.UpstreamError{Provider: s.provider, Message: r.Error}
		}
		chunk := provider.Chunk{
			ID:      "chatcmpl-ollama-" + fmt.Sprint(time.Now().UnixNano()),
			Created: time.Now().Unix(),
			Model:   r.Model,
			Index:   0,
			Role:    r.Message.Role,
			Content: r.Message.Content,
		}
		if r.Done {
			chunk.FinishReason = r.DoneReason
			if chunk.FinishReason == "" {
				chunk.FinishReason = "stop"
			}
			u := &provider.Usage{PromptTokens: r.PromptEvalCount, CompletionTokens: r.EvalCount}
			u.TotalTokens = u.PromptTokens + u.CompletionTokens
			chunk.Usage = u
			s.closed = true
			if s.body != nil {
				_ = s.body.Close()
			}
		}
		if chunk.Content == "" && !r.Done {
			continue
		}
		return chunk, nil
	}
	if err := s.scanner.Err(); err != nil {
		return provider.Chunk{}, &provider.UpstreamError{Provider: s.provider, Message: "ollama stream error", Err: err}
	}
	s.closed = true
	if s.body != nil {
		_ = s.body.Close()
	}
	return provider.Chunk{}, io.EOF
}

func (s *stream) Close() error {
	if s.closed && s.body == nil {
		return nil
	}
	s.closed = true
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
