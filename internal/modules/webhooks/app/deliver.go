// Package app delivers webhooks.
package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"github.com/collectr/collectr/internal/modules/webhooks/domain"
)

// deliveryTimeout bounds one attempt. A receiver that takes longer than this is
// not going to answer.
const deliveryTimeout = 10 * time.Second

// maxResponseSnippet is how much of the receiver's reply is kept.
//
// Error pages routinely echo back what was sent, so the reply can contain the
// personal data we just delivered. Enough to debug with, not enough to become a
// second copy.
const maxResponseSnippet = 1024

// maxRedirects bounds how far a delivery will follow a receiver.
const maxRedirects = 3

// Client posts signed payloads to receiver endpoints.
type Client struct {
	http *http.Client
}

// NewClient returns a Client whose transport refuses to connect to private
// addresses.
//
// The check lives in the dialer, not before the request. Validating the URL when
// it was saved is not enough: DNS can be repointed afterwards, and a redirect can
// send the second request somewhere else entirely. Checking at connect time
// covers the original request, every redirect, and rebinding between resolution
// and connection.
func NewClient() *Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("parsing address: %w", err)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolving %s: %w", host, err)
			}
			for _, ip := range ips {
				if domain.IsPrivateIP(ip.IP) {
					return nil, fmt.Errorf("refusing to connect to %s: private address", ip.IP)
				}
			}
			// Dial the address that was checked, not the hostname: resolving
			// again here would reopen the rebinding window this closes.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		MaxIdleConnsPerHost:   2,
		ResponseHeaderTimeout: deliveryTimeout,
		DisableKeepAlives:     true,
	}

	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   deliveryTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
}

// Result is the outcome of one delivery attempt.
type Result struct {
	StatusCode int
	Snippet    string
	Err        error
}

// Delivered reports whether the receiver accepted the payload.
func (r Result) Delivered() bool {
	return r.Err == nil && r.StatusCode >= 200 && r.StatusCode < 300
}

// Retryable reports whether the attempt is worth repeating.
func (r Result) Retryable() bool {
	if r.Err != nil {
		// A refused connection to a private address is a configuration problem,
		// not a transient one; retrying it forever achieves nothing.
		if errors.Is(r.Err, syscall.ECONNREFUSED) {
			return true
		}
		return r.StatusCode == 0
	}
	return domain.Retryable(r.StatusCode)
}

// Send posts one signed delivery.
func (c *Client) Send(ctx context.Context, url string, secret []byte, eventType, deliveryID string, payload []byte) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Result{Err: fmt.Errorf("building request: %w", err)}
	}

	timestamp := time.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Collectr-Webhook/1")
	req.Header.Set("X-Collectr-Event", eventType)
	req.Header.Set("X-Collectr-Delivery", deliveryID)
	req.Header.Set("X-Collectr-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-Collectr-Signature", Signature(secret, timestamp, payload))

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSnippet))
	return Result{StatusCode: resp.StatusCode, Snippet: string(snippet)}
}

// Signature computes the header a receiver verifies.
//
// The timestamp is inside the signed material, so a captured delivery cannot be
// replayed later: the receiver rejects anything whose timestamp has drifted.
// Signing the body alone would leave that door open.
func Signature(secret []byte, timestamp int64, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte{'.'})
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks a delivery the way a receiver should.
//
// Provided so the documentation can point at a reference implementation: most
// webhook integration bugs are here, in comparing signatures with == or in
// forgetting the timestamp window.
func VerifySignature(secret []byte, header string, timestamp int64, payload []byte, now time.Time, tolerance time.Duration) bool {
	drift := now.Sub(time.Unix(timestamp, 0))
	if drift < 0 {
		drift = -drift
	}
	if drift > tolerance {
		return false
	}
	// Constant time: a byte-by-byte comparison leaks the expected signature
	// through response timing.
	return hmac.Equal([]byte(header), []byte(Signature(secret, timestamp, payload)))
}
