// Package httpx provides shared HTTP primitives used by providers and the
// OpenClaw client: a configured client with per-request timeout and a
// deterministic exponential-backoff retry helper.
package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a configured *http.Client.
type Client struct {
	HTTP *http.Client
}

// NewClient returns a client with the given per-request timeout.
func NewClient(timeout time.Duration) *Client {
	return &Client{HTTP: &http.Client{Timeout: timeout}}
}

// Get performs a GET and returns the response body bytes. The returned *http.Response
// is closed by the caller; body is already read and closed here.
func (c *Client) Get(ctx context.Context, url string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// RetryOption configures Retry.
type RetryOption struct {
	MaxAttempts int           // total attempts (>=1)
	InitialWait time.Duration // base wait between attempts
	MaxWait     time.Duration // upper bound on backoff
	// ShouldRetry decides whether a returned status code should be retried.
	ShouldRetry func(statusCode int) bool
}

// Retry executes fn up to MaxAttempts times with exponential backoff on
// transient errors. fn returns a bool indicating success. Transient = network
// error or a status code matching ShouldRetry. It is deterministic: jitter uses
// a fixed seed derived from attempt index to avoid randomness in outcomes.
func Retry(ctx context.Context, opt RetryOption, fn func() (retry bool, err error)) error {
	if opt.MaxAttempts < 1 {
		opt.MaxAttempts = 1
	}
	if opt.InitialWait <= 0 {
		opt.InitialWait = time.Second
	}
	if opt.MaxWait <= 0 {
		opt.MaxWait = 30 * time.Second
	}
	wait := opt.InitialWait
	for attempt := 1; attempt <= opt.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := fn()
		if ok || err == nil {
			return nil
		}
		if attempt == opt.MaxAttempts {
			return fmt.Errorf("after %d attempts: %w", opt.MaxAttempts, err)
		}
		// Deterministic jitter: scale by attempt index only.
		jitter := time.Duration(attempt) * wait / 2
		sleep := wait + jitter
		if sleep > opt.MaxWait {
			sleep = opt.MaxWait
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		wait *= 2
		if wait > opt.MaxWait {
			wait = opt.MaxWait
		}
	}
	return nil
}
