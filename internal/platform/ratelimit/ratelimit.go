// Package ratelimit throttles public write endpoints.
//
// The failure mode is the design decision here, and it is not the same for every
// endpoint. When Redis is unavailable this limiter can either let everything
// through or refuse everything, and which is right depends entirely on what is
// lost either way -- see Policy.
package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/redisx"
)

// OnBackendFailure decides what happens when the limiter itself cannot answer.
type OnBackendFailure int

const (
	// FailOpen serves the request. Correct where refusing costs something
	// irreplaceable -- a customer's form response -- and the worst case of
	// allowing it is some spam during an outage that is already being paged on.
	FailOpen OnBackendFailure = iota
	// FailClosed refuses the request. Correct where the endpoint guards a
	// disclosure: an unthrottled identify endpoint is an oracle for which people
	// a company holds data on, and that is worse than being briefly unavailable.
	FailClosed
)

// Rule is one limit.
type Rule struct {
	// Name appears in metrics and in the log; keep it stable.
	Name string
	// Limit is how many requests are allowed per Window.
	Limit  int
	Window time.Duration
	// Key extracts what the limit counts. Returning "" skips the rule.
	Key func(*http.Request) string
	// OnFailure is what to do when Redis cannot be reached.
	OnFailure OnBackendFailure
}

// Limiter applies rules using Redis.
type Limiter struct {
	rdb      *redisx.Client
	observed func(rule string)
}

// New returns a Limiter. observed is called whenever a request is refused; it
// may be nil.
func New(rdb *redisx.Client, observed func(rule string)) *Limiter {
	return &Limiter{rdb: rdb, observed: observed}
}

// Allow reports whether one key may proceed under a rule.
//
// A fixed window, not a token bucket: it costs one round trip instead of a Lua
// script, and the burst it permits at a window boundary is irrelevant at the
// limits used here. A sliding window would be the right answer for a limit
// measured in single digits per second.
func (l *Limiter) Allow(ctx context.Context, rule Rule, key string) (allowed bool, remaining int, err error) {
	window := time.Now().UTC().Truncate(rule.Window).Unix()
	redisKey := fmt.Sprintf("rl:%s:%s:%d", rule.Name, key, window)

	// Bounded tightly: a limiter that takes longer than the request it guards is
	// a worse problem than the traffic it is shaping.
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	pipe := l.rdb.Pipeline()
	incr := pipe.Incr(ctx, redisKey)
	// Expiry set every time rather than only on creation: an EXPIRE that fails
	// to land leaves a key counting forever, and the person behind it locked out
	// permanently.
	pipe.Expire(ctx, redisKey, rule.Window+time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, err
	}

	count := int(incr.Val())
	remaining = max(rule.Limit-count, 0)
	return count <= rule.Limit, remaining, nil
}

// Middleware enforces a rule.
func (l *Limiter) Middleware(rule Rule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := rule.Key(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowed, remaining, err := l.Allow(r.Context(), rule, key)
			if err != nil {
				if rule.OnFailure == FailOpen {
					// Loud, because the endpoint is now unprotected and nothing
					// else will say so.
					httpx.Logger(r.Context()).Error(
						"rate limiter unavailable, serving unthrottled",
						"error", err, "rule", rule.Name)
					next.ServeHTTP(w, r)
					return
				}
				httpx.Logger(r.Context()).Error(
					"rate limiter unavailable, refusing request",
					"error", err, "rule", rule.Name)
				retryAfter(w, rule.Window)
				httpx.Error(w, r, http.StatusServiceUnavailable, "unavailable",
					"Tạm thời không xử lý được yêu cầu. Vui lòng thử lại.")
				return
			}

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rule.Limit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

			if !allowed {
				if l.observed != nil {
					l.observed(rule.Name)
				}
				retryAfter(w, rule.Window)
				httpx.Error(w, r, http.StatusTooManyRequests, "rate_limited",
					"Quá nhiều yêu cầu. Vui lòng thử lại sau ít phút.")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Chain applies several rules in order.
func (l *Limiter) Chain(rules ...Rule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		h := next
		// Applied in reverse so the first rule listed is the first evaluated.
		for i := len(rules) - 1; i >= 0; i-- {
			h = l.Middleware(rules[i])(h)
		}
		return h
	}
}

// ByIPPrefix keys a rule on the caller's network rather than their exact
// address.
//
// A /24 rather than a single IP: one household or office behind NAT shares an
// address, and a per-address limit either throttles a whole office or is set so
// high it protects nothing. It also means the limiter never stores a full
// address, matching what the rest of the system does with them.
func ByIPPrefix(r *http.Request) string {
	return httpx.IPPrefix(r)
}

// ByPathValue keys a rule on a route parameter, such as the form being posted
// to. Combined with an address rule this bounds both "one person flooding" and
// "one form being flooded from everywhere".
func ByPathValue(name string) func(*http.Request) string {
	return func(r *http.Request) string { return r.PathValue(name) }
}

func retryAfter(w http.ResponseWriter, window time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
}
