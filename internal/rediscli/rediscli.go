// Package rediscli creates the shared Redis client.
package rediscli

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Options configures Redis connectivity.
type Options struct {
	Addr     string
	Password string
	DB       int
	PoolSize int
}

// New opens and verifies a Redis client.
func New(ctx context.Context, o Options) (*redis.Client, error) {
	if o.PoolSize <= 0 {
		o.PoolSize = 64
	}
	c := redis.NewClient(&redis.Options{
		Addr:         o.Addr,
		Password:     o.Password,
		DB:           o.DB,
		PoolSize:     o.PoolSize,
		MinIdleConns: 8,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolTimeout:  3 * time.Second,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return c, nil
}
