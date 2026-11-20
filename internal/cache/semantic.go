package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/yugrinovD/gogate/internal/observability"
	"github.com/yugrinovD/gogate/internal/store"
)

// Embedder turns text into a fixed-size embedding.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Model() string
	Dimensions() int
}

// Semantic caches non-streaming responses by embedding similarity.
type Semantic struct {
	store     *store.Store
	embedder  Embedder
	threshold float64
	ttl       time.Duration
}

// NewSemantic creates a semantic cache.
func NewSemantic(st *store.Store, embedder Embedder, threshold float64, ttl time.Duration) *Semantic {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.95
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Semantic{store: st, embedder: embedder, threshold: threshold, ttl: ttl}
}

// Lookup returns the closest cached entry above the similarity threshold.
func (s *Semantic) Lookup(ctx context.Context, tenantID, model, prompt string, paramsHash []byte) (*store.SemanticEntry, error) {
	ctx, span := observability.Tracer().Start(ctx, "cache.semantic.get")
	defer span.End()
	vec, err := s.embedder.Embed(ctx, prompt)
	if err != nil {
		return nil, err
	}
	entry, err := s.store.FindSemantic(ctx, tenantID, model, paramsHash, vec, 1-s.threshold)
	if err != nil {
		if store.IsNotFound(err) {
			span.SetAttributes(attribute.Bool("cache.hit", false))
			return nil, nil
		}
		return nil, err
	}
	span.SetAttributes(attribute.Bool("cache.hit", true))
	return &entry, nil
}

// Store persists a response with its embedding.
func (s *Semantic) Store(ctx context.Context, tenantID, model, prompt string, paramsHash []byte, providerName string, promptHash, response, usage []byte, cost float64) error {
	ctx, span := observability.Tracer().Start(ctx, "cache.semantic.set")
	defer span.End()
	vec, err := s.embedder.Embed(ctx, prompt)
	if err != nil {
		return err
	}
	return s.store.InsertSemantic(ctx, tenantID, model, promptHash, paramsHash, providerName, vec, response, usage, cost, time.Now().Add(s.ttl))
}

// MockEmbedder is a deterministic bag-of-words random projection used by demo
// and tests. It produces 768-dimensional normalized vectors.
type MockEmbedder struct{ model string }

// NewMockEmbedder creates a deterministic embedder.
func NewMockEmbedder(model string) *MockEmbedder {
	if model == "" {
		model = "mock-embed"
	}
	return &MockEmbedder{model: model}
}

func (m *MockEmbedder) Model() string   { return m.model }
func (m *MockEmbedder) Dimensions() int { return 768 }

func (m *MockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, 768)
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		v := h.Sum64()
		idx := int(v % 768)
		sign := float32(1)
		if v&(1<<63) != 0 {
			sign = -1
		}
		vec[idx] += sign
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v * v)
	}
	if norm > 0 {
		n := float32(math.Sqrt(norm))
		for i := range vec {
			vec[i] /= n
		}
	}
	return vec, nil
}

// OllamaEmbedder calls Ollama's embedding endpoint.
type OllamaEmbedder struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllamaEmbedder creates an Ollama embedder.
func NewOllamaEmbedder(baseURL, model string, client *http.Client) *OllamaEmbedder {
	if client == nil {
		client = http.DefaultClient
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	return &OllamaEmbedder{baseURL: strings.TrimRight(baseURL, "/"), model: model, client: client}
}

func (o *OllamaEmbedder) Model() string   { return o.model }
func (o *OllamaEmbedder) Dimensions() int { return 768 }

func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": o.model, "input": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ollama embed: status %d", resp.StatusCode)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) == 0 {
		return nil, fmt.Errorf("ollama embed: empty response")
	}
	if len(out.Embeddings[0]) != 768 {
		return nil, fmt.Errorf("ollama embed: expected 768 dimensions, got %d", len(out.Embeddings[0]))
	}
	return out.Embeddings[0], nil
}

// OpenAIEmbedder calls an OpenAI-compatible /embeddings endpoint.
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewOpenAIEmbedder creates an OpenAI-compatible embedder.
func NewOpenAIEmbedder(baseURL, apiKey, model string, client *http.Client) *OpenAIEmbedder {
	if client == nil {
		client = http.DefaultClient
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &OpenAIEmbedder{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model, client: client}
}

func (o *OpenAIEmbedder) Model() string   { return o.model }
func (o *OpenAIEmbedder) Dimensions() int { return 768 }

func (o *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": o.model, "input": text, "dimensions": 768})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai embeddings: status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) != 768 {
		return nil, fmt.Errorf("openai embeddings: expected one 768-dimensional vector")
	}
	return out.Data[0].Embedding, nil
}
