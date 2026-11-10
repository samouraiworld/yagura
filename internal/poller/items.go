package poller

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samourai-crew/yagura/internal/httpx"
)

// itemConcurrency bounds the per-item requests of one poll.
const itemConcurrency = 4

// RunItems calls fn for each item with a concurrency of 4. Metrics returned by
// fn are kept even when fn also returns an error. Errors are classified as
// follows: an auth error (401/403), an httpx.ErrPollDeadline (the rate
// limiter has no time left), or any error once ctx is done, stops the run and
// is returned, which fails the poll; a 404 drops the item silently;
// any other error counts as an item error, including an item timeout while
// ctx is still alive. A panic in fn is recovered and fails the run with an
// "item panicked" error that carries the stack. After a fatal error no new
// item is started and RunItems waits for the in-flight ones. The order of the
// returned metrics is not stable.
func RunItems[T any](ctx context.Context, items []T, fn func(context.Context, T) ([]prometheus.Metric, error)) ([]prometheus.Metric, int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu         sync.Mutex
		metrics    []prometheus.Metric
		itemErrors int
		fatal      error
	)
	record := func(ms []prometheus.Metric, err error) {
		mu.Lock()
		defer mu.Unlock()
		metrics = append(metrics, ms...)
		switch {
		case err == nil, httpx.IsNotFound(err):
		case isPanic(err):
			// A panic is a bug: never hide it behind another fatal error.
			fatal = errors.Join(fatal, err)
			cancel()
		case fatal != nil:
			// Already failing; errors from cancelled siblings do not count.
		case httpx.IsAuthError(err), errors.Is(err, httpx.ErrPollDeadline):
			fatal = err
			cancel()
		case ctx.Err() != nil:
			// The run is over; keep the context error, not a late failure.
			fatal = ctx.Err()
		default:
			// Since Go 1.23 transport timeouts match context.DeadlineExceeded;
			// while the run is alive they belong to this item only.
			itemErrors++
		}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, itemConcurrency)
dispatch:
	for _, it := range items {
		// select picks randomly when both cases are ready, so check the
		// context explicitly before and after acquiring a slot.
		if ctx.Err() != nil {
			break dispatch
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break dispatch
		}
		if ctx.Err() != nil {
			<-sem
			break dispatch
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			record(callItem(ctx, fn, it))
		}()
	}
	wg.Wait()

	if fatal == nil {
		// The parent context may have ended without any item noticing.
		fatal = ctx.Err()
	}
	return metrics, itemErrors, fatal
}

// callItem runs fn and turns a panic into an error. The scheduler's recover
// cannot catch a panic on a RunItems goroutine, which would crash the process.
func callItem[T any](ctx context.Context, fn func(context.Context, T) ([]prometheus.Metric, error), it T) (ms []prometheus.Metric, err error) {
	defer func() {
		if v := recover(); v != nil {
			ms, err = nil, &panicError{msg: fmt.Sprintf("item panicked: %v", v), stack: debug.Stack()}
		}
	}()
	return fn(ctx, it)
}

// panicError is a recovered panic turned into a poll error. stack is the
// panicking goroutine's stack, logged by the scheduler.
type panicError struct {
	msg   string
	stack []byte
}

func (e *panicError) Error() string { return e.msg }

func isPanic(err error) bool {
	_, ok := errors.AsType[*panicError](err)
	return ok
}
