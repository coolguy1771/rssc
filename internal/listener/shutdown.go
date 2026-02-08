package listener

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type ShutdownRunnable struct {
	listenerManager *Manager
	logger          logr.Logger
}

func NewShutdownRunnable(
	listenerManager *Manager,
	logger logr.Logger,
) *ShutdownRunnable {
	return &ShutdownRunnable{
		listenerManager: listenerManager,
		logger:          logger,
	}
}

func (r *ShutdownRunnable) Start(ctx context.Context) error {
	r.logger.Info("Shutdown runnable started")
	<-ctx.Done()
	r.logger.Info("Shutdown signal received, stopping all listeners")
	r.listenerManager.StopAll(ctx)
	return nil
}

var _ manager.Runnable = (*ShutdownRunnable)(nil)
