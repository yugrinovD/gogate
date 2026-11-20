package store

import (
	"encoding/json"
	"time"
)

// Tenant is an isolation boundary for keys, quotas and usage.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// APIKey is a hashed gateway key. The plaintext secret is never stored.
type APIKey struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"key_prefix"`
	Hash       []byte     `json:"-"`
	Enabled    bool       `json:"enabled"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Provider describes an upstream chat provider. Secrets are referenced by
// environment variable name only.
type Provider struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	BaseURL    string          `json:"base_url"`
	APIKeyEnv  string          `json:"api_key_env,omitempty"`
	Models     []string        `json:"models"`
	Priority   int             `json:"priority"`
	Enabled    bool            `json:"enabled"`
	TimeoutMS  int             `json:"timeout_ms"`
	MaxRetries int             `json:"max_retries"`
	Config     json.RawMessage `json:"config"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// Quota holds limits for a tenant or a specific key. A nil pointer means "not set".
type Quota struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	APIKeyID       *string   `json:"api_key_id,omitempty"`
	RPM            *int      `json:"rpm,omitempty"`
	TPM            *int      `json:"tpm,omitempty"`
	DailyRequests  *int64    `json:"daily_requests,omitempty"`
	DailyTokens    *int64    `json:"daily_tokens,omitempty"`
	MonthlyCostUSD *float64  `json:"monthly_cost_usd,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ModelPrice is used to compute request cost.
type ModelPrice struct {
	ID             string    `json:"id"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	InputUSDPer1M  float64   `json:"input_usd_per_1m"`
	OutputUSDPer1M float64   `json:"output_usd_per_1m"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// UsageRecord is a daily aggregate written asynchronously.
type UsageRecord struct {
	TenantID         string
	APIKeyID         string
	Provider         string
	Model            string
	Day              time.Time
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
	CostUSD          float64
}

// AuditRecord stores request metadata only, never prompts or responses.
type AuditRecord struct {
	RequestID        string    `json:"request_id"`
	TenantID         string    `json:"tenant_id"`
	KeyPrefix        string    `json:"key_prefix"`
	Model            string    `json:"model"`
	Provider         string    `json:"provider"`
	Stream           bool      `json:"stream"`
	Status           int       `json:"status"`
	Cache            string    `json:"cache"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	LatencyMS        int       `json:"latency_ms"`
	ErrorCode        string    `json:"error_code"`
	PIIRedacted      bool      `json:"pii_redacted"`
	CreatedAt        time.Time `json:"created_at"`
}

// SemanticEntry is a cached response found by embedding similarity.
type SemanticEntry struct {
	ID         string
	TenantID   string
	Model      string
	PromptHash []byte
	Provider   string
	Response   json.RawMessage
	Usage      json.RawMessage
	CostUSD    float64
	ExpiresAt  time.Time
	Distance   float64
}

// UsageSummary is returned by the admin usage endpoint.
type UsageSummary struct {
	TenantID         string  `json:"tenant_id"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Day              string  `json:"day"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}
