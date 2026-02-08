package client

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type CacheCleanupRunnable struct {
	cache  *Cache
	logger logr.Logger
}

func NewCacheCleanupRunnable(cache *Cache, logger logr.Logger) *CacheCleanupRunnable {
	return &CacheCleanupRunnable{
		cache:  cache,
		logger: logger,
	}
}

func (r *CacheCleanupRunnable) Start(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.cache.Cleanup()
		}
	}
}

var _ manager.Runnable = &CacheCleanupRunnable{}
