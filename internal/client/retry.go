package client

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type RetryConfig struct {
	MaxRetries        int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	BackoffMultiplier float64
}

var DefaultRetryConfig = RetryConfig{
	MaxRetries:        5,
	InitialBackoff:    1 * time.Second,
	MaxBackoff:        30 * time.Second,
	BackoffMultiplier: 2.0,
}

// isRetryableError determines whether RetryWithBackoff will retry on the given error.
// Retry detection expects errors to contain "status=NNN" (matching parsing in
// internal/errors/errors.go). RetryWithBackoff and RetryConfig use this to decide
// retries; the test TestIsRetryableError_StatusCodes constructs errors with
// errors.New("status=XXX") to assert the contract between error string format and retry logic.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	if netErr, ok := err.(net.Error); ok {
		return netErr.Timeout()
	}

	s := err.Error()
	if idx := strings.Index(s, "status="); idx >= 0 {
		rest := s[idx+len("status="):]
		rest = strings.TrimPrefix(rest, "\"")
		if end := strings.IndexAny(rest, "\" )"); end > 0 {
			rest = rest[:end]
		}
		if code, e := strconv.Atoi(rest); e == nil {
			return code >= 500 || code == 429 || code == 408
		}
	}

	return false
}

func RetryWithBackoff(ctx context.Context, config RetryConfig, fn func() error) error {
	var lastErr error
	backoff := config.InitialBackoff

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}

			backoff = time.Duration(float64(backoff) * config.BackoffMultiplier)
			if backoff > config.MaxBackoff {
				backoff = config.MaxBackoff
			}
		}

		err := fn()
		if err == nil {
			return nil
		}

		if !isRetryableError(err) {
			return err
		}

		lastErr = err
	}

	return fmt.Errorf("max retries (%d) exceeded: %w", config.MaxRetries, lastErr)
}
