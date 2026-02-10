package scaler

import (
	"context"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"github.com/go-logr/logr"
)

// DebouncedScaler wraps a listener.Scaler and debounces HandleDesiredRunnerCount calls.
// Rapid calls within the debounce window are coalesced, forwarding only the latest count.
// HandleJobStarted and HandleJobCompleted are passed through immediately.
type DebouncedScaler struct {
	inner interface {
		HandleJobStarted(ctx context.Context, jobInfo *scaleset.JobStarted) error
		HandleJobCompleted(ctx context.Context, jobInfo *scaleset.JobCompleted) error
		HandleDesiredRunnerCount(ctx context.Context, count int) (int, error)
	}
	window time.Duration
	logger logr.Logger

	mu         sync.Mutex
	timer      *time.Timer
	latestCtx  context.Context
	cancelPrev context.CancelFunc
	pending    *int
	resultCh   chan desiredResult
}

type desiredResult struct {
	count int
	err   error
}

func NewDebouncedScaler(
	inner interface {
		HandleJobStarted(ctx context.Context, jobInfo *scaleset.JobStarted) error
		HandleJobCompleted(ctx context.Context, jobInfo *scaleset.JobCompleted) error
		HandleDesiredRunnerCount(ctx context.Context, count int) (int, error)
	},
	window time.Duration,
	logger logr.Logger,
) *DebouncedScaler {
	return &DebouncedScaler{
		inner:  inner,
		window: window,
		logger: logger,
	}
}

func (d *DebouncedScaler) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	d.mu.Lock()

	// Cancel any pending debounce timer
	if d.timer != nil {
		d.timer.Stop()
	}
	if d.cancelPrev != nil {
		d.cancelPrev()
	}

	dctx, cancel := context.WithCancel(ctx)
	d.latestCtx = dctx
	d.cancelPrev = cancel
	d.pending = &count
	d.resultCh = make(chan desiredResult, 1)
	ch := d.resultCh

	d.timer = time.AfterFunc(d.window, func() {
		d.mu.Lock()
		pendingCount := d.pending
		pendingCtx := d.latestCtx
		pendingCh := d.resultCh
		d.pending = nil
		d.mu.Unlock()

		if pendingCount == nil {
			return
		}

		result, err := d.inner.HandleDesiredRunnerCount(pendingCtx, *pendingCount)
		select {
		case pendingCh <- desiredResult{count: result, err: err}:
		default:
		}
	})

	d.mu.Unlock()

	// Wait for the debounced result, our dctx cancellation (when superseded), or caller ctx
	select {
	case res := <-ch:
		return res.count, res.err
	case <-dctx.Done():
		return 0, dctx.Err()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (d *DebouncedScaler) HandleJobStarted(ctx context.Context, jobInfo *scaleset.JobStarted) error {
	return d.inner.HandleJobStarted(ctx, jobInfo)
}

func (d *DebouncedScaler) HandleJobCompleted(ctx context.Context, jobInfo *scaleset.JobCompleted) error {
	return d.inner.HandleJobCompleted(ctx, jobInfo)
}
