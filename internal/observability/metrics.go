// Package observability wires Prometheus metrics and OpenTelemetry tracing.
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds all gateway Prometheus collectors.
type Metrics struct {
	registry *prometheus.Registry

	requests     *prometheus.CounterVec
	duration     *prometheus.HistogramVec
	tokens       *prometheus.CounterVec
	cost         prometheus.Counter
	cacheHits    *prometheus.CounterVec
	rateLimited  *prometheus.CounterVec
	upstreamErrs *prometheus.CounterVec
	inflight     prometheus.Gauge
	retries      *prometheus.CounterVec
	breaker      *prometheus.GaugeVec
	usageDropped prometheus.Counter
	auditDropped prometheus.Counter
	budget       prometheus.Counter
}

// NewMetrics creates and registers collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gogate_requests_total",
			Help: "Total chat requests by provider, model and status.",
		}, []string{"provider", "model", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gogate_request_duration_seconds",
			Help:    "End-to-end chat request duration.",
			Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		}, []string{"provider", "model", "stream"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gogate_tokens_total",
			Help: "Prompt and completion tokens.",
		}, []string{"direction"}),
		cost: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gogate_cost_usd_total",
			Help: "Estimated upstream cost in USD.",
		}),
		cacheHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gogate_cache_hits_total",
			Help: "Cache hits by cache type.",
		}, []string{"type"}),
		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gogate_rate_limited_total",
			Help: "Rate limited requests by scope/reason.",
		}, []string{"scope"}),
		upstreamErrs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gogate_upstream_errors_total",
			Help: "Upstream errors by provider and kind.",
		}, []string{"provider", "kind"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gogate_inflight_requests",
			Help: "Requests currently being processed.",
		}),
		retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gogate_retries_total",
			Help: "Provider retry attempts.",
		}, []string{"provider"}),
		breaker: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gogate_circuit_breaker_state",
			Help: "Circuit breaker state: 0 closed, 1 half-open, 2 open.",
		}, []string{"provider"}),
		usageDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gogate_usage_dropped_total",
			Help: "Usage records dropped because queues were full or flushes failed.",
		}),
		auditDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gogate_audit_dropped_total",
			Help: "Audit records dropped because queues were full or flushes failed.",
		}),
		budget: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gogate_budget_exceeded_total",
			Help: "Requests rejected by cost budgets.",
		}),
	}
	reg.MustRegister(
		m.requests, m.duration, m.tokens, m.cost, m.cacheHits, m.rateLimited,
		m.upstreamErrs, m.inflight, m.retries, m.breaker, m.usageDropped,
		m.auditDropped, m.budget,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler returns the /metrics HTTP handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// IncRequest records a completed request.
func (m *Metrics) IncRequest(provider, model string, status int) {
	m.requests.WithLabelValues(provider, model, strconv.Itoa(status)).Inc()
}

// ObserveDuration records request latency.
func (m *Metrics) ObserveDuration(provider, model string, stream bool, d time.Duration) {
	m.duration.WithLabelValues(provider, model, strconv.FormatBool(stream)).Observe(d.Seconds())
}

// AddTokens records token usage.
func (m *Metrics) AddTokens(direction string, n int) {
	if n < 0 {
		n = 0
	}
	m.tokens.WithLabelValues(direction).Add(float64(n))
}

// AddCost records estimated cost.
func (m *Metrics) AddCost(v float64) {
	if v > 0 {
		m.cost.Add(v)
	}
}

// IncCacheHit records a cache hit.
func (m *Metrics) IncCacheHit(kind string) { m.cacheHits.WithLabelValues(kind).Inc() }

// IncRateLimited records a rejected request.
func (m *Metrics) IncRateLimited(scope string) { m.rateLimited.WithLabelValues(scope).Inc() }

// IncUpstreamError records an upstream failure.
func (m *Metrics) IncUpstreamError(providerName, kind string) {
	m.upstreamErrs.WithLabelValues(providerName, kind).Inc()
}

// IncInflight increments the in-flight gauge.
func (m *Metrics) IncInflight() { m.inflight.Inc() }

// DecInflight decrements the in-flight gauge.
func (m *Metrics) DecInflight() { m.inflight.Dec() }

// IncRetry records a provider retry.
func (m *Metrics) IncRetry(providerName string) { m.retries.WithLabelValues(providerName).Inc() }

// SetBreaker sets breaker state per provider.
func (m *Metrics) SetBreaker(providerName string, state float64) {
	m.breaker.WithLabelValues(providerName).Set(state)
}

// IncUsageDropped records a dropped usage record.
func (m *Metrics) IncUsageDropped() { m.usageDropped.Inc() }

// IncAuditDropped records a dropped audit record.
func (m *Metrics) IncAuditDropped() { m.auditDropped.Inc() }

// IncBudgetExceeded records a budget rejection.
func (m *Metrics) IncBudgetExceeded() { m.budget.Inc() }

// Middleware tracks in-flight requests and optional traces.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.IncInflight()
		defer m.DecInflight()
		next.ServeHTTP(w, r)
	})
}

// Registry exposes the registry for tests.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// TraceMiddleware starts a span for each HTTP request.
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := Tracer().Start(r.Context(), "http.request")
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
