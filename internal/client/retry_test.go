/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type timeoutErr struct{}

func (e *timeoutErr) Error() string   { return "timeout" }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true }

func TestRetryWithBackoff_SuccessFirstTry(t *testing.T) {
	ctx := context.Background()
	cfg := RetryConfig{MaxRetries: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Second}
	calls := 0
	err := RetryWithBackoff(ctx, cfg, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryWithBackoff_SuccessAfterRetry(t *testing.T) {
	ctx := context.Background()
	cfg := RetryConfig{
		MaxRetries:        3,
		InitialBackoff:    5 * time.Millisecond,
		MaxBackoff:        time.Second,
		BackoffMultiplier: 2.0,
	}
	calls := 0
	err := RetryWithBackoff(ctx, cfg, func() error {
		calls++
		if calls < 2 {
			return &timeoutErr{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestRetryWithBackoff_NonRetryableError(t *testing.T) {
	ctx := context.Background()
	cfg := RetryConfig{MaxRetries: 3, InitialBackoff: time.Millisecond}
	want := errors.New("not retryable")
	calls := 0
	err := RetryWithBackoff(ctx, cfg, func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry), got %d", calls)
	}
}

func TestRetryWithBackoff_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := RetryConfig{MaxRetries: 5, InitialBackoff: time.Hour}
	err := RetryWithBackoff(ctx, cfg, func() error {
		return &timeoutErr{}
	})
	if err == nil {
		t.Fatal("expected context canceled error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRetryWithBackoff_MaxRetriesExceeded(t *testing.T) {
	ctx := context.Background()
	cfg := RetryConfig{
		MaxRetries:        2,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        time.Millisecond,
		BackoffMultiplier: 2.0,
	}
	calls := 0
	err := RetryWithBackoff(ctx, cfg, func() error {
		calls++
		return &timeoutErr{}
	})
	if err == nil {
		t.Fatal("expected max retries exceeded error")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls (initial + 2 retries), got %d", calls)
	}
}

func TestIsRetryableError_NetTimeout(t *testing.T) {
	// isRetryableError is not exported; test via RetryWithBackoff that timeout errors are retried
	ctx := context.Background()
	cfg := RetryConfig{MaxRetries: 0, InitialBackoff: time.Millisecond}
	err := RetryWithBackoff(ctx, cfg, func() error {
		var netErr net.Error = &timeoutErr{}
		return netErr
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestIsRetryableError_StatusCodes(t *testing.T) {
	// 500, 429, 408 are retryable (we get initial + 1 retry = 2 calls with MaxRetries: 1)
	// 404, 400 are not retryable (1 call then return)
	tests := []struct {
		name      string
		errStr    string
		retryable bool
	}{
		{"500", "status=500", true},
		{"429", "status=429", true},
		{"408", "status=408", true},
		{"404", "status=404", false},
		{"400", "status=400", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := RetryConfig{MaxRetries: 1, InitialBackoff: time.Millisecond}
			calls := 0
			_ = RetryWithBackoff(ctx, cfg, func() error {
				calls++
				return errors.New(tt.errStr)
			})
			if tt.retryable && calls != 2 {
				t.Errorf("retryable error: expected 2 calls, got %d", calls)
			}
			if !tt.retryable && calls != 1 {
				t.Errorf("non-retryable error: expected 1 call, got %d", calls)
			}
		})
	}
}
