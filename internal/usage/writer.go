// Package usage batches usage and audit writes away from the hot path.
package usage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yugrinovD/gogate/internal/observability"
	"github.com/yugrinovD/gogate/internal/store"
)

// BatchStore is the subset of the store used by the async writer, kept as an
// interface so retry behaviour can be tested without a database.
type BatchStore interface {
	InsertUsage(context.Context, []store.UsageRecord) error
	InsertAudit(context.Context, []store.AuditRecord) error
	TouchAPIKeys(context.Context, []string) error
}

// Writer asynchronously flushes usage and audit batches to PostgreSQL.
type Writer struct {
	store  BatchStore
	log    *slog.Logger
	usage  chan store.UsageRecord
	audit  chan store.AuditRecord
	stop   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
	touch  chan string
	closed atomic.Bool

	onDrop func(kind string)
}

// Options configures queue sizes and flush cadence.
type Options struct {
	QueueSize     int
	BatchSize     int
	FlushInterval time.Duration
	OnDrop        func(kind string)
}

// NewWriter creates a writer and starts its background flushers.
func NewWriter(st BatchStore, log *slog.Logger, opts Options) *Writer {
	if log == nil {
		log = slog.Default()
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 20000
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = time.Second
	}
	w := &Writer{
		store:  st,
		log:    log,
		usage:  make(chan store.UsageRecord, opts.QueueSize),
		audit:  make(chan store.AuditRecord, opts.QueueSize),
		touch:  make(chan string, opts.QueueSize),
		stop:   make(chan struct{}),
		onDrop: opts.OnDrop,
	}
	w.wg.Add(3)
	go w.usageLoop(opts.BatchSize, opts.FlushInterval)
	go w.auditLoop(opts.BatchSize, opts.FlushInterval)
	go w.touchLoop(15 * time.Second)
	return w
}

// SubmitUsage enqueues one usage record without blocking.
func (w *Writer) SubmitUsage(r store.UsageRecord) {
	if w.closed.Load() {
		w.dropUsage()
		return
	}
	select {
	case w.usage <- r:
	default:
		w.dropUsage()
	}
}

// SubmitAudit enqueues one audit record without blocking.
func (w *Writer) SubmitAudit(r store.AuditRecord) {
	if w.closed.Load() {
		w.dropAudit()
		return
	}
	select {
	case w.audit <- r:
	default:
		w.dropAudit()
	}
}

// TouchKey records key usage for periodic last_used_at updates.
func (w *Writer) TouchKey(keyID string) {
	if keyID == "" || w.closed.Load() {
		return
	}
	select {
	case w.touch <- keyID:
	default:
	}
}

// Close drains queues and waits for the final flush.
func (w *Writer) Close(ctx context.Context) error {
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.stop)
	})
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (w *Writer) dropUsage() {
	if w.onDrop != nil {
		w.onDrop("usage")
	}
}

func (w *Writer) dropAudit() {
	if w.onDrop != nil {
		w.onDrop("audit")
	}
}

func (w *Writer) usageLoop(batchSize int, interval time.Duration) {
	defer w.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	buf := make([]store.UsageRecord, 0, batchSize)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		merged := mergeUsage(buf)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ctx, span := observability.Tracer().Start(ctx, "db.usage_flush")
		err := w.store.InsertUsage(ctx, merged)
		span.End()
		cancel()
		if err != nil {
			w.log.Error("usage batch flush failed; retrying individually", "records", len(merged), "error", err)
			w.retryUsage(merged)
		}
		buf = buf[:0]
	}
	for {
		select {
		case r := <-w.usage:
			buf = append(buf, r)
			if len(buf) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-w.stop:
			for {
				select {
				case r := <-w.usage:
					buf = append(buf, r)
					if len(buf) >= batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (w *Writer) auditLoop(batchSize int, interval time.Duration) {
	defer w.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	buf := make([]store.AuditRecord, 0, batchSize)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ctx, span := observability.Tracer().Start(ctx, "db.audit_flush")
		err := w.store.InsertAudit(ctx, buf)
		span.End()
		cancel()
		if err != nil {
			w.log.Error("audit batch flush failed; retrying individually", "records", len(buf), "error", err)
			w.retryAudit(buf)
		}
		buf = buf[:0]
	}
	for {
		select {
		case r := <-w.audit:
			buf = append(buf, r)
			if len(buf) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-w.stop:
			for {
				select {
				case r := <-w.audit:
					buf = append(buf, r)
					if len(buf) >= batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (w *Writer) touchLoop(interval time.Duration) {
	defer w.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	ids := make(map[string]struct{})
	flush := func() {
		if len(ids) == 0 {
			return
		}
		list := make([]string, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := w.store.TouchAPIKeys(ctx, list); err != nil {
			w.log.Warn("last_used_at update failed", "error", err)
		}
		cancel()
		clear(ids)
	}
	for {
		select {
		case id := <-w.touch:
			ids[id] = struct{}{}
		case <-ticker.C:
			flush()
		case <-w.stop:
			flush()
			return
		}
	}
}

func mergeUsage(in []store.UsageRecord) []store.UsageRecord {
	type key struct {
		tenant, keyID, provider, model, day string
	}
	m := make(map[key]store.UsageRecord, len(in))
	order := make([]key, 0, len(in))
	for _, r := range in {
		k := key{r.TenantID, r.APIKeyID, r.Provider, r.Model, r.Day.UTC().Format("2006-01-02")}
		if existing, ok := m[k]; ok {
			existing.Requests += r.Requests
			existing.PromptTokens += r.PromptTokens
			existing.CompletionTokens += r.CompletionTokens
			existing.CostUSD += r.CostUSD
			m[k] = existing
			continue
		}
		m[k] = r
		order = append(order, k)
	}
	out := make([]store.UsageRecord, 0, len(order))
	for _, k := range order {
		out = append(out, m[k])
	}
	return out
}

const retryDelay = 200 * time.Millisecond

func (w *Writer) retryUsage(records []store.UsageRecord) {
	select {
	case <-time.After(retryDelay):
	case <-w.stop:
		for range records {
			w.dropUsage()
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.store.InsertUsage(ctx, records); err != nil {
		w.log.Warn("usage batch dropped after retry", "records", len(records), "error", err)
		for range records {
			w.dropUsage()
		}
	}
}

func (w *Writer) retryAudit(records []store.AuditRecord) {
	select {
	case <-time.After(retryDelay):
	case <-w.stop:
		for range records {
			w.dropAudit()
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.store.InsertAudit(ctx, records); err != nil {
		w.log.Warn("audit batch dropped after retry", "records", len(records), "error", err)
		for range records {
			w.dropAudit()
		}
	}
}
