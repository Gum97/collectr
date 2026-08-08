// Package redisx wraps the Redis client used for caching, rate limiting and the
// analytics event buffer.
//
// Nothing durable lives here. Every caller must keep working when Redis is
// unavailable -- degraded, but working.
package redisx

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client is the shared Redis handle.
type Client struct {
	*redis.Client
}

// Open parses url, connects and verifies the connection.
func Open(ctx context.Context, url string) (*Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parsing redis url: %w", err)
	}
	// The hot path must never wait on Redis longer than it would take to just
	// ask Postgres, so these timeouts are deliberately tight.
	opts.DialTimeout = 2 * time.Second
	opts.ReadTimeout = 200 * time.Millisecond
	opts.WriteTimeout = 200 * time.Millisecond
	opts.PoolSize = 20
	opts.MinIdleConns = 2

	c := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("pinging redis: %w", err)
	}
	return &Client{Client: c}, nil
}

// JitterTTL spreads expiry times by +/-20% so that keys filled together do not
// expire together and stampede the database at the same instant.
func JitterTTL(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	spread := float64(base) * 0.2
	return base + time.Duration((rand.Float64()*2-1)*spread)
}
