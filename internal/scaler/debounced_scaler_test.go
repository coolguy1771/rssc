package scaler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"github.com/go-logr/logr"
)

type mockScaler struct {
	mu                 sync.Mutex
	desiredCalls       []int
	desiredReturnCount int
	desiredReturnErr   error
	jobStartedCalls    int
	jobCompletedCalls  int
	handleDesiredDelay time.Duration
}

func (m *mockScaler) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	if m.handleDesiredDelay > 0 {
		time.Sleep(m.handleDesiredDelay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.desiredCalls = append(m.desiredCalls, count)
	return m.desiredReturnCount, m.desiredReturnErr
}

func (m *mockScaler) HandleJobStarted(ctx context.Context, jobInfo *scaleset.JobStarted) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobStartedCalls++
	return nil
}

func (m *mockScaler) HandleJobCompleted(ctx context.Context, jobInfo *scaleset.JobCompleted) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobCompletedCalls++
	return nil
}

func (m *mockScaler) getDesiredCalls() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]int, len(m.desiredCalls))
	copy(result, m.desiredCalls)
	return result
}

func TestDebouncedScaler_RapidCallsCoalesce(t *testing.T) {
	mock := &mockScaler{desiredReturnCount: 5}
	ds := NewDebouncedScaler(mock, 100*time.Millisecond, logr.Discard())

	ctx := context.Background()
	var wg sync.WaitGroup
	var lastResult atomic.Int32

	// Fire 5 rapid calls with increasing counts
	for i := 1; i <= 5; i++ {
		count := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _ := ds.HandleDesiredRunnerCount(ctx, count)
			lastResult.Store(int32(result))
		}()
		time.Sleep(10 * time.Millisecond) // small delay so each call supersedes the previous
	}

	wg.Wait()

	// Only the last call should have been forwarded to the inner scaler
	calls := mock.getDesiredCalls()
	if len(calls) != 1 {
		t.Errorf("expected 1 forwarded call, got %d: %v", len(calls), calls)
	}
	if len(calls) > 0 && calls[0] != 5 {
		t.Errorf("expected forwarded count to be 5, got %d", calls[0])
	}
}

func TestDebouncedScaler_JobEventsPassThrough(t *testing.T) {
	mock := &mockScaler{}
	ds := NewDebouncedScaler(mock, 100*time.Millisecond, logr.Discard())

	ctx := context.Background()

	if err := ds.HandleJobStarted(ctx, &scaleset.JobStarted{RunnerName: "r1"}); err != nil {
		t.Fatalf("HandleJobStarted returned error: %v", err)
	}
	if err := ds.HandleJobCompleted(ctx, &scaleset.JobCompleted{RunnerName: "r1"}); err != nil {
		t.Fatalf("HandleJobCompleted returned error: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.jobStartedCalls != 1 {
		t.Errorf("expected 1 jobStartedCall, got %d", mock.jobStartedCalls)
	}
	if mock.jobCompletedCalls != 1 {
		t.Errorf("expected 1 jobCompletedCall, got %d", mock.jobCompletedCalls)
	}
}

func TestDebouncedScaler_ContextCancellation(t *testing.T) {
	mock := &mockScaler{desiredReturnCount: 5}
	ds := NewDebouncedScaler(mock, 5*time.Second, logr.Discard())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := ds.HandleDesiredRunnerCount(ctx, 10)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}
