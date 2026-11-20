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
)

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
	log        *slog.Logger
	observer   Observer
	backoff    time.Duration
	maxBackoff time.Duration
}

// Options configures an orchestrator.
type Options struct {
	FirstByteTimeout time.Duration
	BreakerThreshold int
	BreakerCooldown  time.Duration
	BackoffBase      time.Duration
	BackoffMax       time.Duration
	Logger           *slog.Logger
	Observer         Observer
}

// New creates an orchestrator.
func New(manager SnapshotProvider, opts Options) *Orchestrator {
	if opts.FirstByteTimeout <= 0 {
		opts.FirstByteTimeout = 30 * time.Second
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
	cancel   context.CancelFunc
	provider string
	pending  *provider.Chunk
	closed   bool
}

// Provider returns the selected provider name.
func (s *Session) Provider() string { return s.provider }

// Next returns the pending first chunk and then delegates to the provider.
func (s *Session) Next(ctx context.Context) (provider.Chunk, error) {
	if s.closed {
		return provider.Chunk{}, io.EOF
	}
	if s.pending != nil {
		c := *s.pending
		s.pending = nil
		return c, nil
	}
	chunk, err := s.stream.Next(ctx)
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
		if !o.breakers.Allow(p.Name()) {
			continue
		}
		attempted = true
		cfg := configs[p.Name()]
		maxAttempts := cfg.MaxRetries + 1
		if maxAttempts < 1 {
			maxAttempts = 1
		}
		firstByte := cfg.Timeout
		if firstByte <= 0 || firstByte > o.firstByte {
			firstByte = o.firstByte
		}
		for attempt := 0; attempt < maxAttempts; attempt++ {
			if attempt > 0 {
				if o.observer != nil {
					o.observer.ProviderRetry(p.Name(), attempt)
				}
				if err := o.waitBackoff(ctx, attempt); err != nil {
					return nil, err
				}
			}
			attemptCtx, attemptSpan := otel.Tracer("gogate").Start(ctx, "provider.open",
				traceAttrs(p.Name(), req.Model)...)
			streamCtx, cancel := context.WithCancel(attemptCtx)
			timer := time.AfterFunc(firstByte, cancel)
			stream, err := p.Chat(streamCtx, req)
			if err != nil {
				timer.Stop()
				cancel()
				attemptSpan.RecordError(err)
				attemptSpan.End()
				if isClientCanceled(ctx, err) {
					return nil, err
				}
				o.breakers.Failure(p.Name())
				if o.observer != nil {
					o.observer.ProviderFailure(p.Name(), err)
				}
				lastErr = err
				if !retryable(err) {
					break
				}
				continue
			}
			chunk, err := stream.Next(streamCtx)
			stopped := timer.Stop()
			if err != nil {
				_ = stream.Close()
				cancel()
				attemptSpan.RecordError(err)
				attemptSpan.End()
				if isClientCanceled(ctx, err) {
					return nil, err
				}
				o.breakers.Failure(p.Name())
				if o.observer != nil {
					o.observer.ProviderFailure(p.Name(), err)
				}
				lastErr = err
				if !retryable(err) {
					break
				}
				continue
			}
			if !stopped && streamCtx.Err() != nil {
				_ = stream.Close()
				cancel()
				lastErr = fmt.Errorf("%w: first byte timeout", ErrUpstreamFailed)
				attemptSpan.RecordError(lastErr)
				attemptSpan.End()
				o.breakers.Failure(p.Name())
				continue
			}
			o.breakers.Success(p.Name())
			attemptSpan.SetAttributes(attribute.Bool("committed", true))
			attemptSpan.End()
			return &Session{
				stream:   stream,
				cancel:   cancel,
				provider: p.Name(),
				pending:  &chunk,
			}, nil
		}
		// This provider is exhausted; try the next candidate.
	}
	if !attempted {
		return nil, ErrAllProvidersUnavailable
	}
	if lastErr == nil {
		lastErr = ErrUpstreamFailed
	}
	return nil, fmt.Errorf("%w: %w", ErrUpstreamFailed, lastErr)
}

func (o *Orchestrator) waitBackoff(ctx context.Context, attempt int) error {
	d := o.backoff << (attempt - 1)
	if d > o.maxBackoff || d <= 0 {
		d = o.maxBackoff
	}
	jitter := time.Duration(rand.Int64N(int64(d/2) + 1))
	d = d/2 + jitter
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

func isClientCanceled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled
}

func traceAttrs(providerName, model string) []trace.SpanStartOption {
	return []trace.SpanStartOption{trace.WithAttributes(
		attribute.String("provider", providerName),
		attribute.String("model", model),
	)}
}
