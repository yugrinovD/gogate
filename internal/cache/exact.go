// Package cache implements exact Redis caching and semantic pgvector caching.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/redis/go-redis/v9"
	"github.com/yugrinovD/gogate/internal/observability"
	"github.com/yugrinovD/gogate/internal/provider"
)

// CacheVersion invalidates cached entries when the key schema or redaction
// semantics change.
const CacheVersion = "v2"

// KeyInput contains all fields that affect a non-streaming response.
type KeyInput struct {
	TenantID         string
	Model            string
	Messages         []provider.Message
	Temperature      *float64
	TopP             *float64
	MaxTokens        *int
	MaxCompletion    *int
	Stop             []string
	Seed             *int
	PresencePenalty  *float64
	FrequencyPenalty *float64
	PIIRedaction     bool
}

// CachedResponse is the stored representation.
type CachedResponse struct {
	Response json.RawMessage `json:"response"`
	Usage    provider.Usage  `json:"usage"`
	Provider string          `json:"provider"`
	CostUSD  float64         `json:"cost_usd"`
}

// Exact is a Redis exact-match cache.
type Exact struct {
	client *redis.Client
	ttl    time.Duration
}

// NewExact creates an exact cache.
func NewExact(client *redis.Client, ttl time.Duration) *Exact {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Exact{client: client, ttl: ttl}
}

// Key computes the cache key without requiring a Redis client.
func Key(in KeyInput) (key string, promptHash []byte, prompt string) {
	payload := struct {
		Version          string             `json:"version"`
		TenantID         string             `json:"tenant_id"`
		Model            string             `json:"model"`
		Messages         []provider.Message `json:"messages"`
		Temperature      *float64           `json:"temperature"`
		TopP             *float64           `json:"top_p"`
		MaxTokens        *int               `json:"max_tokens"`
		MaxCompletion    *int               `json:"max_completion_tokens"`
		Stop             []string           `json:"stop"`
		Seed             *int               `json:"seed"`
		PresencePenalty  *float64           `json:"presence_penalty"`
		FrequencyPenalty *float64           `json:"frequency_penalty"`
		PIIRedaction     bool               `json:"pii_redaction"`
	}{
		Version: CacheVersion, TenantID: in.TenantID, Model: in.Model, Messages: in.Messages,
		Temperature: in.Temperature, TopP: in.TopP, MaxTokens: in.MaxTokens,
		MaxCompletion: in.MaxCompletion, Stop: in.Stop, Seed: in.Seed,
		PresencePenalty: in.PresencePenalty, FrequencyPenalty: in.FrequencyPenalty,
		PIIRedaction: in.PIIRedaction,
	}
	body, _ := json.Marshal(payload)
	sum := sha256.Sum256(body)
	return "gogate:cache:chat:" + hex.EncodeToString(sum[:]), sum[:], CanonicalPrompt(in.Messages)
}

// ParamsHash hashes generation-affecting parameters without messages. It is
// used to stop semantic cache from mixing responses produced with different
// sampling or token limits.
func ParamsHash(in KeyInput) []byte {
	payload := struct {
		Version          string   `json:"version"`
		Model            string   `json:"model"`
		Temperature      *float64 `json:"temperature"`
		TopP             *float64 `json:"top_p"`
		MaxTokens        *int     `json:"max_tokens"`
		MaxCompletion    *int     `json:"max_completion_tokens"`
		Stop             []string `json:"stop"`
		Seed             *int     `json:"seed"`
		PresencePenalty  *float64 `json:"presence_penalty"`
		FrequencyPenalty *float64 `json:"frequency_penalty"`
		PIIRedaction     bool     `json:"pii_redaction"`
	}{
		Version: CacheVersion, Model: in.Model, Temperature: in.Temperature, TopP: in.TopP,
		MaxTokens: in.MaxTokens, MaxCompletion: in.MaxCompletion, Stop: in.Stop, Seed: in.Seed,
		PresencePenalty: in.PresencePenalty, FrequencyPenalty: in.FrequencyPenalty, PIIRedaction: in.PIIRedaction,
	}
	body, _ := json.Marshal(payload)
	sum := sha256.Sum256(body)
	return sum[:]
}

// CanonicalPrompt produces the text used for semantic embeddings.
func CanonicalPrompt(messages []provider.Message) string {
	var buf []byte
	for _, m := range messages {
		buf = append(buf, m.Role...)
		buf = append(buf, ':', ' ')
		buf = append(buf, m.Content...)
		buf = append(buf, '\n')
	}
	return string(buf)
}

// Get returns a cached response.
func (e *Exact) Get(ctx context.Context, key string) (*CachedResponse, error) {
	ctx, span := observability.Tracer().Start(ctx, "cache.exact.get")
	defer span.End()
	raw, err := e.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			span.SetAttributes(attribute.Bool("cache.hit", false))
			return nil, nil
		}
		return nil, err
	}
	span.SetAttributes(attribute.Bool("cache.hit", true))
	var out CachedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode cached response: %w", err)
	}
	return &out, nil
}

// Set stores a response.
func (e *Exact) Set(ctx context.Context, key string, value CachedResponse) error {
	ctx, span := observability.Tracer().Start(ctx, "cache.exact.set")
	defer span.End()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return e.client.Set(ctx, key, raw, e.ttl).Err()
}

// Delete removes a cache entry.
func (e *Exact) Delete(ctx context.Context, key string) error {
	return e.client.Del(ctx, key).Err()
}

// Flush deletes all chat cache entries using SCAN.
func (e *Exact) Flush(ctx context.Context) (int64, error) {
	if e == nil || e.client == nil {
		return 0, nil
	}
	var cursor uint64
	var deleted int64
	for {
		keys, next, err := e.client.Scan(ctx, cursor, "gogate:cache:chat:*", 500).Result()
		if err != nil {
			return deleted, err
		}
		if len(keys) > 0 {
			n, err := e.client.Del(ctx, keys...).Result()
			if err != nil {
				return deleted, err
			}
			deleted += n
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return deleted, nil
}
