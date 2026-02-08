package client

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiter_Wait(t *testing.T) {
	// Create a rate limiter with 10 req/s, burst 2
	rl := NewRateLimiter(10, 2)

	ctx := context.Background()

	// First 2 calls should succeed immediately (burst)
	start := time.Now()
	for i := 0; i < 2; i++ {
		if err := rl.Wait(ctx); err != nil {
			t.Fatalf("Wait() returned error on call %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Errorf("burst calls took too long: %v", elapsed)
	}

	// Third call should be delayed
	start = time.Now()
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("Wait() returned error on burst+1 call: %v", err)
	}
	elapsed = time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Errorf("expected delay after burst exhaustion, got %v", elapsed)
	}
}

func TestRateLimiter_AllowDepletes(t *testing.T) {
	rl := NewRateLimiter(1, 2)

	// First 2 calls should be allowed (burst)
	if !rl.Allow() {
		t.Error("expected Allow() to return true for first call")
	}
	if !rl.Allow() {
		t.Error("expected Allow() to return true for second call (burst)")
	}

	// Third call should be denied (tokens depleted)
	if rl.Allow() {
		t.Error("expected Allow() to return false after burst exhaustion")
	}
}

func TestRateLimiter_WaitCancelled(t *testing.T) {
	// Very slow limiter
	rl := NewRateLimiter(0.1, 1)
	// Exhaust burst
	rl.Allow()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := rl.Wait(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestNewRateLimiter_ZeroBurst(t *testing.T) {
	// When burst is 0, it should default to at least 1
	rl := NewRateLimiter(0.5, 0)
	if !rl.Allow() {
		t.Error("expected Allow() to succeed with auto-calculated burst")
	}
}
