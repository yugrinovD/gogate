// Package config loads and validates gogate configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the complete runtime configuration. Secrets are only read from the
// environment and are never persisted by the gateway itself.
type Config struct {
	Env         string
	PublicAddr  string
	AdminAddr   string
	AdminToken  string
	DatabaseURL string

	RedisAddr     string
	RedisPassword string
	RedisDB       int

	Pepper string

	MaxBodyBytes      int64
	MaxOutputTokens   int
	MaxMessages       int
	MaxInflight       int
	RequestTimeout    time.Duration
	FirstByteTimeout  time.Duration
	StreamIdleTimeout time.Duration

	CacheEnabled bool
	CacheTTL     time.Duration

	SemanticCacheEnabled bool
	EmbeddingProvider    string
	EmbeddingModel       string
	SemanticThreshold    float64

	RateLimitEnabled  bool
	RateLimitFailOpen bool

	PIIRedactionEnabled bool
	AuditEnabled        bool
	BudgetEnabled       bool
	AuditRetention      time.Duration

	BootstrapProviders bool
	OllamaBaseURL      string
	OllamaModels       []string
	OpenAIBaseURL      string
	OpenAIKeyEnv       string
	OpenAIModels       []string
	MockModels         []string
	DefaultOllamaModel string

	MetricsEnabled bool
	TracingEnabled bool
	OTLPEndpoint   string
	LogLevel       string
	AccessLogRate  float64
	PprofEnabled   bool

	CORSOrigins          []string
	AllowPrivateUpstream bool

	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	DatabaseMaxConns  int32
	RedisPoolSize     int
}

// Load reads configuration from the environment, applies defaults and validates
// required values.
func Load() (Config, error) {
	cfg := Config{
		Env:         envString("GOGATE_ENV", "dev"),
		PublicAddr:  envString("GOGATE_PUBLIC_ADDR", ":8080"),
		AdminAddr:   envString("GOGATE_ADMIN_ADDR", ":9090"),
		AdminToken:  envString("GOGATE_ADMIN_TOKEN", ""),
		DatabaseURL: envString("GOGATE_DATABASE_URL", "postgres://gogate:gogate@127.0.0.1:5432/gogate?sslmode=disable"),

		RedisAddr:     envString("GOGATE_REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword: envString("GOGATE_REDIS_PASSWORD", ""),
		RedisDB:       envInt("GOGATE_REDIS_DB", 0),

		Pepper: envString("GOGATE_API_KEY_PEPPER", ""),

		MaxBodyBytes:      envInt64("GOGATE_MAX_BODY_BYTES", 1<<20),
		MaxOutputTokens:   envInt("GOGATE_MAX_OUTPUT_TOKENS", 4096),
		MaxMessages:       envInt("GOGATE_MAX_MESSAGES", 128),
		MaxInflight:       envInt("GOGATE_MAX_INFLIGHT", 4096),
		RequestTimeout:    envDuration("GOGATE_REQUEST_TIMEOUT", 120*time.Second),
		FirstByteTimeout:  envDuration("GOGATE_FIRST_BYTE_TIMEOUT", 30*time.Second),
		StreamIdleTimeout: envDuration("GOGATE_STREAM_IDLE_TIMEOUT", 60*time.Second),

		CacheEnabled: envBool("GOGATE_CACHE_ENABLED", true),
		CacheTTL:     envDuration("GOGATE_CACHE_TTL", 5*time.Minute),

		SemanticCacheEnabled: envBool("GOGATE_SEMANTIC_CACHE_ENABLED", false),
		EmbeddingProvider:    envString("GOGATE_EMBEDDING_PROVIDER", "mock"),
		EmbeddingModel:       envString("GOGATE_EMBEDDING_MODEL", "mock-embed"),
		SemanticThreshold:    envFloat("GOGATE_SEMANTIC_THRESHOLD", 0.95),

		RateLimitEnabled:  envBool("GOGATE_RATE_LIMIT_ENABLED", true),
		RateLimitFailOpen: envBool("GOGATE_RATE_LIMIT_FAIL_OPEN", false),

		PIIRedactionEnabled: envBool("GOGATE_PII_REDACT", false),
		AuditEnabled:        envBool("GOGATE_AUDIT_ENABLED", true),
		BudgetEnabled:       envBool("GOGATE_BUDGET_ENABLED", true),
		AuditRetention:      envDuration("GOGATE_AUDIT_RETENTION", 30*24*time.Hour),

		BootstrapProviders: envBool("GOGATE_BOOTSTRAP_PROVIDERS", true),
		OllamaBaseURL:      envString("GOGATE_OLLAMA_BASE_URL", ""),
		OllamaModels:       envList("GOGATE_OLLAMA_MODELS", []string{"qwen2.5:3b"}),
		OpenAIBaseURL:      envString("GOGATE_OPENAI_BASE_URL", ""),
		OpenAIKeyEnv:       envString("GOGATE_OPENAI_KEY_ENV", "GOGATE_OPENAI_API_KEY"),
		OpenAIModels:       envList("GOGATE_OPENAI_MODELS", nil),
		MockModels:         envList("GOGATE_MOCK_MODELS", []string{"mock", "mock-fast"}),
		DefaultOllamaModel: envString("GOGATE_DEFAULT_OLLAMA_MODEL", "qwen2.5:3b"),

		MetricsEnabled: envBool("GOGATE_METRICS_ENABLED", true),
		TracingEnabled: envBool("GOGATE_TRACING_ENABLED", false),
		OTLPEndpoint:   envString("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		LogLevel:       envString("GOGATE_LOG_LEVEL", "info"),
		AccessLogRate:  envFloat("GOGATE_ACCESS_LOG_SAMPLE", 1.0),
		PprofEnabled:   envBool("GOGATE_PPROF_ENABLED", false),

		CORSOrigins:          envList("GOGATE_CORS_ALLOWED_ORIGINS", nil),
		AllowPrivateUpstream: envBool("GOGATE_ALLOW_PRIVATE_UPSTREAMS", true),

		ReadHeaderTimeout: envDuration("GOGATE_READ_HEADER_TIMEOUT", 5*time.Second),
		IdleTimeout:       envDuration("GOGATE_IDLE_TIMEOUT", 120*time.Second),
		DatabaseMaxConns:  clampInt32(envInt("GOGATE_DATABASE_MAX_CONNS", 20), 1, 1000),
		RedisPoolSize:     envInt("GOGATE_REDIS_POOL_SIZE", 64),
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks required values and bounds.
func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.Pepper) == "" {
		errs = append(errs, errors.New("GOGATE_API_KEY_PEPPER is required"))
	}
	if len(c.Pepper) < 16 {
		errs = append(errs, errors.New("GOGATE_API_KEY_PEPPER must be at least 16 characters"))
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		errs = append(errs, errors.New("GOGATE_DATABASE_URL is required"))
	}
	if c.PublicAddr == c.AdminAddr && c.AdminToken != "" {
		errs = append(errs, errors.New("public and admin listeners must use different addresses"))
	}
	if c.MaxBodyBytes <= 0 {
		errs = append(errs, errors.New("GOGATE_MAX_BODY_BYTES must be positive"))
	}
	if c.MaxOutputTokens <= 0 {
		errs = append(errs, errors.New("GOGATE_MAX_OUTPUT_TOKENS must be positive"))
	}
	if c.SemanticThreshold <= 0 || c.SemanticThreshold > 1 {
		errs = append(errs, errors.New("GOGATE_SEMANTIC_THRESHOLD must be in (0,1]"))
	}
	if c.AccessLogRate < 0 || c.AccessLogRate > 1 {
		errs = append(errs, errors.New("GOGATE_ACCESS_LOG_SAMPLE must be in [0,1]"))
	}
	return errors.Join(errs...)
}

// AdminEnabled reports whether the separate admin listener should run.
func (c Config) AdminEnabled() bool { return strings.TrimSpace(c.AdminToken) != "" }

// Redacted returns a safe representation for admin diagnostics.
func (c Config) Redacted() map[string]any {
	return map[string]any{
		"env":                    c.Env,
		"public_addr":            c.PublicAddr,
		"admin_addr":             c.AdminAddr,
		"admin_enabled":          c.AdminEnabled(),
		"redis_addr":             c.RedisAddr,
		"max_body_bytes":         c.MaxBodyBytes,
		"max_output_tokens":      c.MaxOutputTokens,
		"request_timeout":        c.RequestTimeout.String(),
		"first_byte_timeout":     c.FirstByteTimeout.String(),
		"stream_idle_timeout":    c.StreamIdleTimeout.String(),
		"cache_enabled":          c.CacheEnabled,
		"semantic_cache_enabled": c.SemanticCacheEnabled,
		"rate_limit_enabled":     c.RateLimitEnabled,
		"pii_redact":             c.PIIRedactionEnabled,
		"audit_enabled":          c.AuditEnabled,
		"budget_enabled":         c.BudgetEnabled,
		"metrics_enabled":        c.MetricsEnabled,
		"tracing_enabled":        c.TracingEnabled,
		"log_level":              c.LogLevel,
		"bootstrap_providers":    c.BootstrapProviders,
	}
}

func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envInt64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		if n, nerr := strconv.Atoi(v); nerr == nil {
			return time.Duration(n) * time.Second
		}
		return def
	}
	return d
}

func envList(key string, def []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// String implements fmt.Stringer without exposing secrets.
func (c Config) String() string {
	return fmt.Sprintf("gogate env=%s public=%s admin=%s", c.Env, c.PublicAddr, c.AdminAddr)
}

func clampInt32(v, min, max int) int32 {
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return int32(v) //nolint:gosec // bounded by clampInt32 callers
}
