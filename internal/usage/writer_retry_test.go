package usage

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/yugrinovD/gogate/internal/store"
)

type fakeBatchStore struct {
	usageCalls int
	auditCalls int
	usageFail  int
	auditFail  int
}

func (f *fakeBatchStore) InsertUsage(context.Context, []store.UsageRecord) error {
	f.usageCalls++
	if f.usageCalls <= f.usageFail {
		return context.DeadlineExceeded
	}
	return nil
}

func (f *fakeBatchStore) InsertAudit(context.Context, []store.AuditRecord) error {
	f.auditCalls++
	if f.auditCalls <= f.auditFail {
		return context.DeadlineExceeded
	}
	return nil
}

func (f *fakeBatchStore) TouchAPIKeys(context.Context, []string) error { return nil }

func TestUsageRetryUsesOneBatchAttempt(t *testing.T) {
	fake := &fakeBatchStore{usageFail: 1}
	dropped := 0
	w := &Writer{
		store:  fake,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		stop:   make(chan struct{}),
		onDrop: func(string) { dropped++ },
	}
	_ = fake.InsertUsage(context.Background(), []store.UsageRecord{{}, {}})
	w.retryUsage([]store.UsageRecord{{}, {}})
	if fake.usageCalls != 2 {
		t.Fatalf("usage calls=%d", fake.usageCalls)
	}
	if dropped != 0 {
		t.Fatalf("dropped=%d", dropped)
	}
}

func TestUsageRetryDropsWholeBatch(t *testing.T) {
	fake := &fakeBatchStore{usageFail: 2}
	dropped := 0
	w := &Writer{
		store:  fake,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		stop:   make(chan struct{}),
		onDrop: func(string) { dropped++ },
	}
	_ = fake.InsertUsage(context.Background(), []store.UsageRecord{{}, {}, {}})
	w.retryUsage([]store.UsageRecord{{}, {}, {}})
	if fake.usageCalls != 2 {
		t.Fatalf("usage calls=%d", fake.usageCalls)
	}
	if dropped != 3 {
		t.Fatalf("dropped=%d", dropped)
	}
}
