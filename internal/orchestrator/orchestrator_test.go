package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

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
