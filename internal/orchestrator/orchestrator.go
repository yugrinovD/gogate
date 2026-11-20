// Package orchestrator owns provider selection, retries, fallback and the
// first-byte commit boundary.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/yugrinovD/gogate/internal/breaker"
	"github.com/yugrinovD/gogate/internal/control"
	"github.com/yugrinovD/gogate/internal/provider"
)

var (
	// ErrModelNotFound means no enabled provider exposes the requested model.
	ErrModelNotFound = errors.New("model not found")
	// ErrAllProvidersUnavailable means every candidate breaker is open.
	ErrAllProvidersUnavailable = errors.New("all providers unavailable")
	// ErrUpstreamFailed means all provider attempts failed before the first byte.
	ErrUpstreamFailed = errors.New("all providers failed")
	// ErrFirstByteTimeout means the upstream did not produce a first chunk in time.
	ErrFirstByteTimeout = errors.New("first byte timeout")
	// ErrStreamIdle means an established upstream stream stopped producing chunks.
	ErrStreamIdle  = errors.New("upstream stream idle")
	errBreakerOpen = errors.New("provider breaker is open")
)

const maxProviderRetries = 5

// Observer receives orchestration events for metrics/logging.
type Observer interface {
	ProviderRetry(providerName string, attempt int)
	ProviderFailure(providerName string, err error)
}

// SnapshotProvider supplies the current control-plane snapshot.
type SnapshotProvider interface {
	Snapshot() *control.Snapshot
}

// Orchestrator selects providers from a control-plane manager.
type Orchestrator struct {
	manager    SnapshotProvider
	breakers   *breaker.Manager
	firstByte  time.Duration
	streamIdle time.Duration
	log        *slog.Logger
	observer   Observer
	backoff    time.Duration
	maxBackoff time.Duration
}

// Options configures an orchestrator.
type Options struct {
	FirstByteTimeout  time.Duration
	StreamIdleTimeout time.Duration
	BreakerThreshold  int
	BreakerCooldown   time.Duration
	BackoffBase       time.Duration
	BackoffMax        time.Duration
	Logger            *slog.Logger
	Observer          Observer
}

// New creates an orchestrator.
func New(manager SnapshotProvider, opts Options) *Orchestrator {
	if opts.FirstByteTimeout <= 0 {
		opts.FirstByteTimeout = 30 * time.Second
	}
	if opts.StreamIdleTimeout <= 0 {
		opts.StreamIdleTimeout = 60 * time.Second
	}
	if opts.BackoffBase <= 0 {
		opts.BackoffBase = 100 * time.Millisecond
	}
	if opts.BackoffMax <= 0 {
		opts.BackoffMax = 2 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Orchestrator{
		manager:    manager,
		breakers:   breaker.New(opts.BreakerThreshold, opts.BreakerCooldown),
		firstByte:  opts.FirstByteTimeout,
		streamIdle: opts.StreamIdleTimeout,
		log:        opts.Logger,
		observer:   opts.Observer,
		backoff:    opts.BackoffBase,
		maxBackoff: opts.BackoffMax,
	}
}

// Breakers exposes breaker state for admin health and metrics.
func (o *Orchestrator) Breakers() *breaker.Manager { return o.breakers }

// Session is a committed provider stream. After Open returns successfully the
// first byte has been received, so fallback is no longer allowed.
type Session struct {
	stream   provider.Stream
	ctx      context.Context
	cancel   context.CancelFunc
	provider string
	pending  *provider.Chunk
	idle     time.Duration
	closed   bool
}

// Provider returns the selected provider name.
func (s *Session) Provider() string { return s.provider }

// Next returns the pending first chunk and then delegates to the provider.
// Once the stream is committed, an idle upstream is aborted rather than held
// forever.
func (s *Session) Next(ctx context.Context) (provider.Chunk, error) {
	if s.closed {
		return provider.Chunk{}, io.EOF
	}
	if s.pending != nil {
		c := *s.pending
		s.pending = nil
		return c, nil
	}
	runCtx := s.ctx
	if runCtx == nil {
		runCtx = ctx
	}
	if s.idle <= 0 {
		return s.stream.Next(runCtx)
	}
	timer := time.AfterFunc(s.idle, s.cancel)
	chunk, err := s.stream.Next(runCtx)
	if !timer.Stop() {
		if ctx.Err() != nil {
			return chunk, ctx.Err()
		}
		_ = s.stream.Close()
		s.closed = true
		return chunk, fmt.Errorf("%w after %s", ErrStreamIdle, s.idle)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return chunk, err
	}
	return chunk, err
}

// Close releases the upstream stream.
func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.stream != nil {
		return s.stream.Close()
	}
	return nil
}

// Open selects a provider and consumes exactly one chunk to establish the
// first-byte boundary. Failures before this point are retried and/or failed over.
func (o *Orchestrator) Open(ctx context.Context, req provider.Request) (*Session, error) {
	snap := o.manager.Snapshot()
	if snap == nil {
		return nil, fmt.Errorf("%w: control plane not ready", ErrAllProvidersUnavailable)
	}
	candidates := snap.Providers.ForModel(req.Model)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrModelNotFound, req.Model)
	}
	configs := snap.Providers.Configs()
	var lastErr error
	attempted := false

	for _, p := range candidates {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		sess, err := o.tryProvider(ctx, p, configs[p.Name()], req)
		if err == nil {
			return sess, nil
		}
		if errors.Is(err, errBreakerOpen) {
			continue
		}
		attempted = true
		if isClientCanceled(ctx, err) {
			return nil, err
		}
		lastErr = err
	}
	if !attempted {
		return nil, ErrAllProvidersUnavailable
	}
	if lastErr == nil {
		lastErr = ErrUpstreamFailed
	}
	return nil, fmt.Errorf("%w: %w", ErrUpstreamFailed, lastErr)
}

func (o *Orchestrator) tryProvider(ctx context.Context, p provider.Provider, cfg provider.Config, req provider.Request) (*Session, error) {
	if !o.breakers.Allow(p.Name()) {
		return nil, errBreakerOpen
	}
	resolved := false
	defer func() {
		if !resolved {
			o.breakers.Release(p.Name())
		}
	}()

	maxAttempts := cfg.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if maxAttempts > maxProviderRetries+1 {
		maxAttempts = maxProviderRetries + 1
	}
	firstByte := cfg.Timeout
	if firstByte <= 0 || firstByte > o.firstByte {
		firstByte = o.firstByte
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt > 0 {
			if o.observer != nil {
				o.observer.ProviderRetry(p.Name(), attempt)
			}
			if err := o.waitBackoff(ctx, attempt, retryAfterFrom(lastErr)); err != nil {
				return nil, err
			}
		}
		attemptCtx, attemptSpan := otel.Tracer("gogate").Start(ctx, "provider.open",
			traceAttrs(p.Name(), req.Model)...)
		streamCtx, cancel := context.WithCancel(attemptCtx)
		timer := time.AfterFunc(firstByte, cancel)
		stream, err := p.Chat(streamCtx, req)
		if err != nil {
			stopped := timer.Stop()
			cancel()
			lastErr = err
			if !stopped && ctx.Err() == nil {
				lastErr = fmt.Errorf("%w after %s", ErrFirstByteTimeout, firstByte)
			}
			attemptSpan.RecordError(lastErr)
			attemptSpan.End()
			if isClientCanceled(ctx, err) {
				return nil, err
			}
			o.recordFailure(p.Name(), lastErr)
			resolved = true
			if o.observer != nil {
				o.observer.ProviderFailure(p.Name(), lastErr)
			}
			if errors.Is(lastErr, ErrFirstByteTimeout) || retryable(err) {
				continue
			}
			o.log.Debug("provider returned a non-retryable error", "provider", p.Name(), "error", err)
			break
		}
		chunk, err := stream.Next(streamCtx)
		stopped := timer.Stop()
		if err != nil {
			_ = stream.Close()
			cancel()
			lastErr = err
			if !stopped && ctx.Err() == nil {
				lastErr = fmt.Errorf("%w after %s", ErrFirstByteTimeout, firstByte)
			}
			attemptSpan.RecordError(lastErr)
			attemptSpan.End()
			if isClientCanceled(ctx, err) {
				return nil, err
			}
			o.recordFailure(p.Name(), lastErr)
			resolved = true
			if o.observer != nil {
				o.observer.ProviderFailure(p.Name(), lastErr)
			}
			if errors.Is(lastErr, ErrFirstByteTimeout) || retryable(err) {
				continue
			}
			break
		}
		if !stopped && streamCtx.Err() != nil {
			_ = stream.Close()
			cancel()
			lastErr = fmt.Errorf("%w after %s", ErrFirstByteTimeout, firstByte)
			attemptSpan.RecordError(lastErr)
			attemptSpan.End()
			o.breakers.Failure(p.Name())
			resolved = true
			if o.observer != nil {
				o.observer.ProviderFailure(p.Name(), lastErr)
			}
			continue
		}
		o.breakers.Success(p.Name())
		attemptSpan.SetAttributes(attribute.Bool("committed", true))
		attemptSpan.End()
		resolved = true
		return &Session{
			stream:   stream,
			ctx:      streamCtx,
			cancel:   cancel,
			provider: p.Name(),
			pending:  &chunk,
			idle:     o.streamIdle,
		}, nil
	}
	if lastErr == nil {
		lastErr = ErrUpstreamFailed
	}
	return nil, lastErr
}

func (o *Orchestrator) recordFailure(providerName string, err error) {
	switch {
	case shouldTripBreaker(err):
		o.breakers.Failure(providerName)
	case isUpstream4xx(err):
		// The provider answered; it is reachable and should not be penalised.
		o.breakers.Success(providerName)
	default:
		o.breakers.Release(providerName)
	}
}

const maxRetryAfter = 30 * time.Second

func backoffDuration(attempt int, base, max, retryAfter time.Duration) time.Duration {
	d := base << (attempt - 1)
	if d > max || d <= 0 {
		d = max
	}
	if retryAfter > maxRetryAfter {
		retryAfter = maxRetryAfter
	}
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

func (o *Orchestrator) waitBackoff(ctx context.Context, attempt int, retryAfter time.Duration) error {
	d := backoffDuration(attempt, o.backoff, o.maxBackoff, retryAfter)
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= d {
		return context.DeadlineExceeded
	}
	if retryAfter <= 0 {
		jitter := time.Duration(rand.Int64N(int64(d/2) + 1))
		d = d/2 + jitter
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	var ue *provider.UpstreamError
	if errors.As(err, &ue) {
		return ue.Retryable()
	}
	return true
}

func shouldTripBreaker(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var ue *provider.UpstreamError
	if errors.As(err, &ue) {
		if ue.StatusCode == 0 {
			return true
		}
		if ue.StatusCode == 408 || ue.StatusCode == 425 || ue.StatusCode == 429 {
			return true
		}
		return ue.StatusCode >= 500
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return true
}

func isUpstream4xx(err error) bool {
	var ue *provider.UpstreamError
	if errors.As(err, &ue) {
		return ue.StatusCode >= 400 && ue.StatusCode < 500
	}
	return false
}

func retryAfterFrom(err error) time.Duration {
	var ue *provider.UpstreamError
	if errors.As(err, &ue) {
		return ue.RetryAfter
	}
	return 0
}

func isClientCanceled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled
}

func traceAttrs(providerName, model string) []trace.SpanStartOption {
	return []trace.SpanStartOption{trace.WithAttributes(
		attribute.String("provider", providerName),
		attribute.String("model", model),
	)}
}

// ErrorKind classifies errors for metrics and logs. It is shared by the
// orchestrator observer and HTTP handler so both use one label vocabulary.
func ErrorKind(err error) string {
	var ue *provider.UpstreamError
	if errors.As(err, &ue) {
		switch {
		case ue.StatusCode == 429:
			return "rate_limited"
		case ue.StatusCode >= 500:
			return "upstream_5xx"
		case ue.StatusCode >= 400:
			return "upstream_4xx"
		case ue.StatusCode == 0:
			return "network"
		}
	}
	switch {
	case errors.Is(err, ErrFirstByteTimeout), errors.Is(err, ErrStreamIdle), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "error"
	}
}
