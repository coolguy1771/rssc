package client

import (
	"context"

	"golang.org/x/time/rate"
)

type RateLimiter struct {
	limiter *rate.Limiter
}

func NewRateLimiter(requestsPerSecond float64, burst int) *RateLimiter {
	if burst <= 0 {
		burst = int(requestsPerSecond)
		if burst < 1 {
			burst = 1
		}
	}
	return &RateLimiter{
		limiter: rate.NewLimiter(
			rate.Limit(requestsPerSecond),
			burst,
		),
	}
}

func (r *RateLimiter) Wait(ctx context.Context) error {
	return r.limiter.Wait(ctx)
}

func (r *RateLimiter) Allow() bool {
	return r.limiter.Allow()
}

type RateLimitedClient struct {
	client  interface{}
	limiter *RateLimiter
}

func NewRateLimitedClient(
	client interface{},
	requestsPerSecond float64,
	burst int,
) *RateLimitedClient {
	return &RateLimitedClient{
		client:  client,
		limiter: NewRateLimiter(requestsPerSecond, burst),
	}
}

func (r *RateLimitedClient) Wait(ctx context.Context) error {
	return r.limiter.Wait(ctx)
}
