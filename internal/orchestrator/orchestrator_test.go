package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/yugrinovD/gogate/internal/breaker"
	"github.com/yugrinovD/gogate/internal/control"
	"github.com/yugrinovD/gogate/internal/provider"
	"github.com/yugrinovD/gogate/internal/provider/registry"
)

type fakeSnap struct{ snap *control.Snapshot }

func (f fakeSnap) Snapshot() *control.Snapshot { return f.snap }

func newOrch(t *testing.T, configs []provider.Config, opts Options) *Orchestrator {
	t.Helper()
	reg := registry.NewRegistry(configs, registry.Options{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		AllowPrivate: true,
	})
	snap := &control.Snapshot{Providers: reg}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.FirstByteTimeout == 0 {
		opts.FirstByteTimeout = time.Second
	}
	return New(fakeSnap{snap: snap}, opts)
}

func mockConfig(name, model string, priority int, cfg map[string]any) provider.Config {
	raw, _ := json.Marshal(cfg)
	return provider.Config{Name: name, Kind: "mock", Models: []string{model}, Priority: priority, RawConfig: raw}
}

func TestFallbackBeforeFirstByte(t *testing.T) {
	orch := newOrch(t, []provider.Config{
		mockConfig("bad", "fallback", 1, map[string]any{"fail_before_first": true}),
		mockConfig("good", "fallback", 2, map[string]any{"chunk_delay_ms": 0}),
	}, Options{})
	sess, err := orch.Open(context.Background(), provider.Request{
		Model: "fallback", Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if sess.Provider() != "good" {
		t.Fatalf("provider=%s", sess.Provider())
	}
	chunk, err := sess.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Content == "" {
		t.Fatal("expected content")
	}
}

func TestNoFallbackAfterFirstByte(t *testing.T) {
	orch := newOrch(t, []provider.Config{
		mockConfig("flaky", "flaky", 1, map[string]any{"chunk_delay_ms": 0, "fail_after_chunks": 1}),
	}, Options{})
	sess, err := orch.Open(context.Background(), provider.Request{
		Model: "flaky", Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if sess.Provider() != "flaky" {
		t.Fatalf("provider=%s", sess.Provider())
	}
	if _, err := sess.Next(context.Background()); err != nil {
		t.Fatal("first byte should be committed")
	}
	if _, err := sess.Next(context.Background()); err == nil {
		t.Fatal("expected terminal stream error, not fallback")
	}
}

func TestModelNotFound(t *testing.T) {
	orch := newOrch(t, []provider.Config{
		mockConfig("mock", "mock", 1, nil),
	}, Options{})
	_, err := orch.Open(context.Background(), provider.Request{Model: "missing"})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestBreakerSkipsProvider(t *testing.T) {
	orch := newOrch(t, []provider.Config{
		mockConfig("bad", "bad", 1, map[string]any{"fail_before_first": true}),
	}, Options{BreakerThreshold: 1})
	req := provider.Request{Model: "bad", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	if _, err := orch.Open(context.Background(), req); !errors.Is(err, ErrUpstreamFailed) {
		t.Fatalf("first error=%v", err)
	}
	if _, err := orch.Open(context.Background(), req); !errors.Is(err, ErrAllProvidersUnavailable) {
		t.Fatalf("second error=%v", err)
	}
}

func TestUpstream4xxDoesNotTripBreaker(t *testing.T) {
	orch := newOrch(t, []provider.Config{
		mockConfig("bad-request", "bad-request", 1, map[string]any{"fail_before_first": true, "fail_status": 400}),
	}, Options{BreakerThreshold: 1})
	req := provider.Request{Model: "bad-request", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	if _, err := orch.Open(context.Background(), req); !errors.Is(err, ErrUpstreamFailed) {
		t.Fatalf("first error=%v", err)
	}
	if _, err := orch.Open(context.Background(), req); !errors.Is(err, ErrUpstreamFailed) {
		t.Fatalf("4xx must not open the breaker, got %v", err)
	}
}

func TestUpstream5xxTripsBreaker(t *testing.T) {
	orch := newOrch(t, []provider.Config{
		mockConfig("server-error", "server-error", 1, map[string]any{"fail_before_first": true, "fail_status": 500}),
	}, Options{BreakerThreshold: 1})
	req := provider.Request{Model: "server-error", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	if _, err := orch.Open(context.Background(), req); !errors.Is(err, ErrUpstreamFailed) {
		t.Fatalf("first error=%v", err)
	}
	if _, err := orch.Open(context.Background(), req); !errors.Is(err, ErrAllProvidersUnavailable) {
		t.Fatalf("5xx should open the breaker, got %v", err)
	}
}

func TestStreamIdleTimeout(t *testing.T) {
	orch := newOrch(t, []provider.Config{
		mockConfig("slow", "slow", 1, map[string]any{"chunk_delay_ms": 200}),
	}, Options{StreamIdleTimeout: 20 * time.Millisecond})
	sess, err := orch.Open(context.Background(), provider.Request{
		Model: "slow", Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if _, err := sess.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = sess.Next(context.Background())
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("expected stream idle, got %v", err)
	}
	if time.Since(start) > 150*time.Millisecond {
		t.Fatalf("idle timeout did not cancel promptly: %s", time.Since(start))
	}
}

func TestFirstByteTimeoutSentinel(t *testing.T) {
	orch := newOrch(t, []provider.Config{
		mockConfig("slow-first", "slow-first", 1, map[string]any{"latency_ms": 200}),
	}, Options{FirstByteTimeout: 20 * time.Millisecond, BreakerThreshold: 1})
	_, err := orch.Open(context.Background(), provider.Request{
		Model: "slow-first", Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, ErrFirstByteTimeout) {
		t.Fatalf("expected first-byte timeout, got %v", err)
	}
}

func TestClientCancelDoesNotTripBreaker(t *testing.T) {
	orch := newOrch(t, []provider.Config{
		mockConfig("slow-cancel", "slow-cancel", 1, map[string]any{"latency_ms": 1000}),
	}, Options{BreakerThreshold: 1, FirstByteTimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := orch.Open(ctx, provider.Request{
		Model: "slow-cancel", Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if state := orch.Breakers().State("slow-cancel"); state != breaker.StateClosed {
		t.Fatalf("client cancel tripped breaker: %s", state)
	}
	if !orch.Breakers().Allow("slow-cancel") {
		t.Fatal("provider should still be allowed")
	}
}

func TestShouldTripBreakerIgnoresCanceled(t *testing.T) {
	if shouldTripBreaker(&provider.UpstreamError{Err: context.Canceled}) {
		t.Fatal("canceled upstream error must not trip breaker")
	}
	if shouldTripBreaker(&provider.UpstreamError{StatusCode: 500}) {
		// expected true, checked below
	} else {
		t.Fatal("5xx must trip breaker")
	}
}

func TestBackoffDuration(t *testing.T) {
	base := 100 * time.Millisecond
	max := 2 * time.Second
	if got := backoffDuration(1, base, max, 0); got != base {
		t.Fatalf("attempt 1=%s", got)
	}
	if got := backoffDuration(10, base, max, 0); got != max {
		t.Fatalf("cap=%s", got)
	}
	if got := backoffDuration(1, base, max, 10*time.Second); got != 10*time.Second {
		t.Fatalf("retry-after=%s", got)
	}
	if got := backoffDuration(1, base, max, 60*time.Second); got != maxRetryAfter {
		t.Fatalf("retry-after cap=%s", got)
	}
}
