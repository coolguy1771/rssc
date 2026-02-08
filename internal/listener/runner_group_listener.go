package listener

import (
	"context"
	"log/slog"
	"sync"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/go-logr/logr"
)

type RunnerGroupListener struct {
	client   listener.Client
	listener *listener.Listener
	scaler   listener.Scaler
	stopCh   chan struct{}
	stopped  sync.Once
	logger   logr.Logger
}

func NewRunnerGroupListener(
	client *scaleset.MessageSessionClient,
	scaleSetID int,
	maxRunners int,
	scaler listener.Scaler,
	logger logr.Logger,
) (*RunnerGroupListener, error) {
	listenerClient := &messageSessionClientWrapper{
		client: client,
	}

	slogLogger := slog.New(&logrHandler{logger: logger})
	l, err := listener.New(
		listenerClient,
		listener.Config{
			ScaleSetID: scaleSetID,
			MaxRunners: maxRunners,
			Logger:     slogLogger,
		},
	)
	if err != nil {
		return nil, err
	}

	return &RunnerGroupListener{
		client:   listenerClient,
		listener: l,
		scaler:   scaler,
		stopCh:   make(chan struct{}),
		logger:   logger,
	}, nil
}

func (r *RunnerGroupListener) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-r.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	return r.listener.Run(ctx, r.scaler)
}

func (r *RunnerGroupListener) Stop() {
	r.stopped.Do(func() {
		close(r.stopCh)
	})
}

type messageSessionClientWrapper struct {
	client *scaleset.MessageSessionClient
}

func (m *messageSessionClientWrapper) GetMessage(
	ctx context.Context,
	lastMessageID, maxCapacity int,
) (*scaleset.RunnerScaleSetMessage, error) {
	return m.client.GetMessage(ctx, lastMessageID, maxCapacity)
}

func (m *messageSessionClientWrapper) DeleteMessage(
	ctx context.Context,
	messageID int,
) error {
	return m.client.DeleteMessage(ctx, messageID)
}

func (m *messageSessionClientWrapper) Session() scaleset.RunnerScaleSetSession {
	return m.client.Session()
}

type logrHandler struct {
	logger logr.Logger
}

func (h *logrHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *logrHandler) Handle(ctx context.Context, record slog.Record) error {
	attrs := make([]interface{}, 0, record.NumAttrs())
	record.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a.Key, a.Value.Any())
		return true
	})
	h.logger.Info(record.Message, attrs...)
	return nil
}

func (h *logrHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *logrHandler) WithGroup(name string) slog.Handler {
	return h
}
