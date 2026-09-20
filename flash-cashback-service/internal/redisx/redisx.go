// Package redisx wraps Redis usage. Redis is deliberately NOT authoritative for
// any money decision — it is used only for per-user rate limiting and short-TTL
// caching of read-only campaign status. Every method degrades gracefully: if
// Redis is unavailable, rate limiting fails open (requests are allowed) rather
// than blocking money movement, and caching simply misses.
package redisx

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	rdb *redis.Client
}

// New connects to Redis. A failed ping is not fatal: the service can run without
// Redis (rate limiting fails open, cache misses). Returns a Client that is nil
// only if construction itself fails.
func New(ctx context.Context, addr, password string, db int) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})
	return &Client{rdb: rdb}
}

func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Ping(ctx).Err()
}

func (c *Client) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

// Allow implements a simple fixed-window rate limiter: at most `limit` events
// per `window` for the given key. It fails open — any Redis error returns
// (allowed=true) so availability is never sacrificed for rate limiting.
func (c *Client) Allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	if c == nil || c.rdb == nil || limit <= 0 {
		return true
	}
	pipe := c.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return true // fail open
	}
	return incr.Val() <= int64(limit)
}
