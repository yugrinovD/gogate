package control

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// ReloadChannel notifies all gateway replicas that the control plane changed.
const ReloadChannel = "gogate:control:reload"

// PublishReload asks other replicas to refresh their snapshots.
func PublishReload(ctx context.Context, rdb *redis.Client) error {
	if rdb == nil {
		return nil
	}
	return rdb.Publish(ctx, ReloadChannel, "1").Err()
}

// PeriodicReload refreshes the control plane on a fixed interval so revocations
// eventually reach every replica even without pub/sub.
func PeriodicReload(ctx context.Context, manager *Manager, interval time.Duration, log *slog.Logger) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := manager.Reload(ctx); err != nil {
				log.Warn("periodic control plane reload failed", "error", err)
			}
		}
	}
}

// SubscribeReload listens for admin-triggered reload notifications.
func SubscribeReload(ctx context.Context, rdb *redis.Client, manager *Manager, log *slog.Logger) {
	if rdb == nil {
		return
	}
	pubsub := rdb.Subscribe(ctx, ReloadChannel)
	defer func() { _ = pubsub.Close() }()
	channel := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-channel:
			if !ok {
				return
			}
			if err := manager.Reload(ctx); err != nil {
				log.Warn("pub/sub control plane reload failed", "error", err)
			}
		}
	}
}
