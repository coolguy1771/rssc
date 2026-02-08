package listener

import (
	"context"
	"fmt"
	"sync"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
)

type Manager struct {
	mu        sync.RWMutex
	listeners map[ListenerKey]*RunnerGroupListener
	logger    logr.Logger
}

type ListenerKey struct {
	AutoscaleSet types.NamespacedName
	RunnerGroup  string
}

func NewManager(logger logr.Logger) *Manager {
	return &Manager{
		listeners: make(map[ListenerKey]*RunnerGroupListener),
		logger:    logger,
	}
}

func (m *Manager) StartListener(
	ctx context.Context,
	key ListenerKey,
	sessionClient *scaleset.MessageSessionClient,
	scaleSetID int,
	maxRunners int,
	scaler listener.Scaler,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.listeners[key]; exists {
		return fmt.Errorf("listener already exists for %+v", key)
	}

	runnerListener, err := NewRunnerGroupListener(
		sessionClient,
		scaleSetID,
		maxRunners,
		scaler,
		m.logger.WithValues(
			"autoscaleSet", key.AutoscaleSet.String(),
			"runnerGroup", key.RunnerGroup,
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	m.listeners[key] = runnerListener

	go func() {
		if err := runnerListener.Run(ctx); err != nil {
			m.logger.Error(
				err,
				"Listener stopped with error",
				"key", key,
			)
		}
		m.mu.Lock()
		delete(m.listeners, key)
		m.mu.Unlock()
	}()

	return nil
}

func (m *Manager) StopListener(key ListenerKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	runnerListener, exists := m.listeners[key]
	if !exists {
		return fmt.Errorf("listener not found for %+v", key)
	}

	runnerListener.Stop()
	delete(m.listeners, key)

	return nil
}

func (m *Manager) HasListener(key ListenerKey) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.listeners[key]
	return exists
}

func (m *Manager) StopAll(ctx context.Context) {
	m.mu.Lock()
	count := len(m.listeners)
	listeners := make([]*RunnerGroupListener, 0, count)
	for key, runnerListener := range m.listeners {
		listeners = append(listeners, runnerListener)
		delete(m.listeners, key)
	}
	m.mu.Unlock()

	if count == 0 {
		return
	}

	m.logger.Info(
		"Stopping all listeners",
		"count", count,
	)

	for _, runnerListener := range listeners {
		select {
		case <-ctx.Done():
			m.logger.Info("Shutdown context cancelled")
			return
		default:
		}
		runnerListener.Stop()
	}

	m.logger.Info("All listeners signaled to stop")
}
