// Package config loads and validates gogate configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yugrinovD/gogate/internal/security"
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

	MaxBodyBytes    int64
	MaxOutputTokens int
	MaxMessages     int
	MaxInflight     int
	MaxAPIKeyBytes  int

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

	ControlReloadInterval time.Duration
	UpstreamKeyEnvPrefix  string

	BootstrapProviders bool
	OllamaBaseURL      string
	OllamaModels       []string
	OpenAIBaseURL      string
	OpenAIKeyEnv       string
	OpenAIModels       []string
	MockModels         []string

	MetricsEnabled bool
	TracingEnabled bool
	TraceSample    float64
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
// required values. Malformed typed values are reported instead of silently
// falling back to defaults.
func Load() (Config, error) {
	l := &loader{}
	cfg := Config{
		Env:         l.String("GOGATE_ENV", "dev"),
		PublicAddr:  l.String("GOGATE_PUBLIC_ADDR", ":8080"),
		AdminAddr:   l.String("GOGATE_ADMIN_ADDR", ":9090"),
		AdminToken:  l.String("GOGATE_ADMIN_TOKEN", ""),
		DatabaseURL: l.String("GOGATE_DATABASE_URL", "postgres://gogate:gogate@127.0.0.1:5432/gogate?sslmode=disable"),

		RedisAddr:     l.String("GOGATE_REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword: l.String("GOGATE_REDIS_PASSWORD", ""),
		RedisDB:       l.Int("GOGATE_REDIS_DB", 0),

		Pepper: l.String("GOGATE_API_KEY_PEPPER", ""),

		MaxBodyBytes:    l.Int64("GOGATE_MAX_BODY_BYTES", 1<<20),
		MaxOutputTokens: l.Int("GOGATE_MAX_OUTPUT_TOKENS", 4096),
		MaxMessages:     l.Int("GOGATE_MAX_MESSAGES", 128),
		MaxInflight:     l.Int("GOGATE_MAX_INFLIGHT", 4096),
		MaxAPIKeyBytes:  l.Int("GOGATE_MAX_API_KEY_BYTES", 256),

		RequestTimeout:    l.Duration("GOGATE_REQUEST_TIMEOUT", 120*time.Second),
		FirstByteTimeout:  l.Duration("GOGATE_FIRST_BYTE_TIMEOUT", 30*time.Second),
		StreamIdleTimeout: l.Duration("GOGATE_STREAM_IDLE_TIMEOUT", 60*time.Second),

		CacheEnabled: l.Bool("GOGATE_CACHE_ENABLED", true),
		CacheTTL:     l.Duration("GOGATE_CACHE_TTL", 5*time.Minute),

		SemanticCacheEnabled: l.Bool("GOGATE_SEMANTIC_CACHE_ENABLED", false),
		EmbeddingProvider:    strings.ToLower(strings.TrimSpace(l.String("GOGATE_EMBEDDING_PROVIDER", "mock"))),
		EmbeddingModel:       l.String("GOGATE_EMBEDDING_MODEL", "mock-embed"),
		SemanticThreshold:    l.Float("GOGATE_SEMANTIC_THRESHOLD", 0.95),

		RateLimitEnabled:  l.Bool("GOGATE_RATE_LIMIT_ENABLED", true),
		RateLimitFailOpen: l.Bool("GOGATE_RATE_LIMIT_FAIL_OPEN", false),

		PIIRedactionEnabled: l.Bool("GOGATE_PII_REDACT", false),
		AuditEnabled:        l.Bool("GOGATE_AUDIT_ENABLED", true),
		BudgetEnabled:       l.Bool("GOGATE_BUDGET_ENABLED", true),
		AuditRetention:      l.Duration("GOGATE_AUDIT_RETENTION", 30*24*time.Hour),

		ControlReloadInterval: l.Duration("GOGATE_CONTROL_RELOAD_INTERVAL", 30*time.Second),
		UpstreamKeyEnvPrefix:  l.String("GOGATE_UPSTREAM_KEY_PREFIX", "GOGATE_UPSTREAM_"),

		BootstrapProviders: l.Bool("GOGATE_BOOTSTRAP_PROVIDERS", true),
		OllamaBaseURL:      l.String("GOGATE_OLLAMA_BASE_URL", ""),
		OllamaModels:       l.List("GOGATE_OLLAMA_MODELS", []string{"qwen2.5:3b"}),
		OpenAIBaseURL:      l.String("GOGATE_OPENAI_BASE_URL", ""),
		OpenAIKeyEnv:       l.String("GOGATE_OPENAI_KEY_ENV", "GOGATE_UPSTREAM_OPENAI_API_KEY"),
		OpenAIModels:       l.List("GOGATE_OPENAI_MODELS", nil),
		MockModels:         l.List("GOGATE_MOCK_MODELS", []string{"mock", "mock-fast"}),

		MetricsEnabled: l.Bool("GOGATE_METRICS_ENABLED", true),
		TracingEnabled: l.Bool("GOGATE_TRACING_ENABLED", false),
		TraceSample:    l.Float("GOGATE_TRACE_SAMPLE_RATIO", 1.0),
		OTLPEndpoint:   l.String("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		LogLevel:       l.String("GOGATE_LOG_LEVEL", "info"),
		AccessLogRate:  l.Float("GOGATE_ACCESS_LOG_SAMPLE", 1.0),
		PprofEnabled:   l.Bool("GOGATE_PPROF_ENABLED", false),

		CORSOrigins:          l.List("GOGATE_CORS_ALLOWED_ORIGINS", nil),
		AllowPrivateUpstream: l.Bool("GOGATE_ALLOW_PRIVATE_UPSTREAMS", false),

		ReadHeaderTimeout: l.Duration("GOGATE_READ_HEADER_TIMEOUT", 5*time.Second),
		IdleTimeout:       l.Duration("GOGATE_IDLE_TIMEOUT", 120*time.Second),
		DatabaseMaxConns:  clampInt32(l.Int("GOGATE_DATABASE_MAX_CONNS", 20), 1, 1000),
		RedisPoolSize:     l.Int("GOGATE_REDIS_POOL_SIZE", 64),
	}

	if err := errors.Join(l.errs...); err != nil {
		return Config{}, err
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
	if c.MaxMessages <= 0 {
		errs = append(errs, errors.New("GOGATE_MAX_MESSAGES must be positive"))
	}
	if c.MaxInflight <= 0 {
		errs = append(errs, errors.New("GOGATE_MAX_INFLIGHT must be positive"))
	}
	if c.MaxAPIKeyBytes < security.MinKeyBytes(c.Env) {
		errs = append(errs, fmt.Errorf("GOGATE_MAX_API_KEY_BYTES must be at least %d for env %q", security.MinKeyBytes(c.Env), c.Env))
	}
	if c.RequestTimeout <= 0 || c.FirstByteTimeout <= 0 || c.StreamIdleTimeout <= 0 {
		errs = append(errs, errors.New("request, first-byte and stream-idle timeouts must be positive"))
	}
	if c.CacheTTL <= 0 {
		errs = append(errs, errors.New("GOGATE_CACHE_TTL must be positive"))
	}
	if c.ControlReloadInterval < 0 {
		errs = append(errs, errors.New("GOGATE_CONTROL_RELOAD_INTERVAL must be >= 0 (0 disables periodic reload)"))
	}
	if strings.TrimSpace(c.UpstreamKeyEnvPrefix) == "" || !strings.HasSuffix(c.UpstreamKeyEnvPrefix, "_") {
		errs = append(errs, errors.New("GOGATE_UPSTREAM_KEY_PREFIX must be non-empty and end with '_'"))
	}
	if strings.TrimSpace(c.OpenAIBaseURL) != "" && strings.TrimSpace(c.OpenAIKeyEnv) != "" {
		if err := security.ValidateEnvName(c.OpenAIKeyEnv, c.UpstreamKeyEnvPrefix); err != nil && !security.IsLegacyUpstreamEnv(c.OpenAIKeyEnv) {
			errs = append(errs, fmt.Errorf("GOGATE_OPENAI_KEY_ENV is invalid: %w", err))
		}
	}
	if c.SemanticThreshold <= 0 || c.SemanticThreshold > 1 {
		errs = append(errs, errors.New("GOGATE_SEMANTIC_THRESHOLD must be in (0,1]"))
	}
	if c.TraceSample < 0 || c.TraceSample > 1 {
		errs = append(errs, errors.New("GOGATE_TRACE_SAMPLE_RATIO must be in [0,1]"))
	}
	if c.AccessLogRate < 0 || c.AccessLogRate > 1 {
		errs = append(errs, errors.New("GOGATE_ACCESS_LOG_SAMPLE must be in [0,1]"))
	}
	if c.RedisPoolSize <= 0 {
		errs = append(errs, errors.New("GOGATE_REDIS_POOL_SIZE must be positive"))
	}
	if c.ReadHeaderTimeout <= 0 || c.IdleTimeout <= 0 {
		errs = append(errs, errors.New("HTTP header and idle timeouts must be positive"))
	}
	if c.AuditRetention <= 0 {
		errs = append(errs, errors.New("GOGATE_AUDIT_RETENTION must be positive"))
	}
	if c.SemanticCacheEnabled {
		switch strings.ToLower(c.EmbeddingProvider) {
		case "mock":
			if c.SecureEnv() {
				errs = append(errs, errors.New("semantic cache with the mock embedder is not allowed in production"))
			}
		case "ollama":
			if strings.TrimSpace(c.OllamaBaseURL) == "" {
				errs = append(errs, errors.New("GOGATE_OLLAMA_BASE_URL is required for the ollama embedder"))
			}
		case "openai":
			if strings.TrimSpace(c.OpenAIBaseURL) == "" {
				errs = append(errs, errors.New("GOGATE_OPENAI_BASE_URL is required for the openai embedder"))
			}
		default:
			errs = append(errs, fmt.Errorf("unsupported GOGATE_EMBEDDING_PROVIDER %q (use mock, ollama or openai)", c.EmbeddingProvider))
		}
	}
	return errors.Join(errs...)
}

// AdminEnabled reports whether the separate admin listener should run.
func (c Config) AdminEnabled() bool { return strings.TrimSpace(c.AdminToken) != "" }

// SecureEnv reports whether production security checks should be strict.
func (c Config) SecureEnv() bool {
	return strings.EqualFold(c.Env, "prod") || strings.EqualFold(c.Env, "production")
}

// Redacted returns a safe representation for admin diagnostics.
func (c Config) Redacted() map[string]any {
	return map[string]any{
		"env":                     c.Env,
		"public_addr":             c.PublicAddr,
		"admin_addr":              c.AdminAddr,
		"admin_enabled":           c.AdminEnabled(),
		"redis_addr":              c.RedisAddr,
		"max_body_bytes":          c.MaxBodyBytes,
		"max_output_tokens":       c.MaxOutputTokens,
		"max_api_key_bytes":       c.MaxAPIKeyBytes,
		"request_timeout":         c.RequestTimeout.String(),
		"first_byte_timeout":      c.FirstByteTimeout.String(),
		"stream_idle_timeout":     c.StreamIdleTimeout.String(),
		"cache_enabled":           c.CacheEnabled,
		"semantic_cache_enabled":  c.SemanticCacheEnabled,
		"rate_limit_enabled":      c.RateLimitEnabled,
		"rate_limit_fail_open":    c.RateLimitFailOpen,
		"pii_redact":              c.PIIRedactionEnabled,
		"audit_enabled":           c.AuditEnabled,
		"budget_enabled":          c.BudgetEnabled,
		"control_reload_interval": c.ControlReloadInterval.String(),
		"metrics_enabled":         c.MetricsEnabled,
		"tracing_enabled":         c.TracingEnabled,
		"trace_sample_ratio":      c.TraceSample,
		"log_level":               c.LogLevel,
		"bootstrap_providers":     c.BootstrapProviders,
		"allow_private_upstream":  c.AllowPrivateUpstream,
	}
}

// String implements fmt.Stringer without exposing secrets.
func (c Config) String() string {
	return fmt.Sprintf("gogate env=%s public=%s admin=%s", c.Env, c.PublicAddr, c.AdminAddr)
}

type loader struct {
	errs []error
}

func (l *loader) String(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func (l *loader) Bool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid bool %q", key, v))
		return def
	}
	return b
}

func (l *loader) Int(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid integer %q", key, v))
		return def
	}
	return n
}

func (l *loader) Int64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid integer %q", key, v))
		return def
	}
	return n
}

func (l *loader) Float(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid float %q", key, v))
		return def
	}
	return f
}

func (l *loader) Duration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		if n, nerr := strconv.Atoi(v); nerr == nil {
			return time.Duration(n) * time.Second
		}
		l.errs = append(l.errs, fmt.Errorf("%s: invalid duration %q", key, v))
		return def
	}
	return d
}

func (l *loader) List(key string, def []string) []string {
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

func clampInt32(v, min, max int) int32 {
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return int32(v) //nolint:gosec // bounded by clampInt32 callers
}
