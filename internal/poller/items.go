package poller

import (
	"context"
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samourai-crew/yagura/internal/httpx"
)

// itemConcurrency bounds the per-item requests of one poll.
const itemConcurrency = 4

// RunItems calls fn for each item with a concurrency of 4. Metrics returned by
// fn are kept even when fn also returns an error. Errors are classified as
// follows: a context error or an auth error (401/403) stops the run and is
// returned, which fails the poll; a 404 drops the item silently; any other
// error counts as an item error. After a fatal error no new item is started
// and RunItems waits for the in-flight ones. The order of the returned
// metrics is not stable.
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
		case fatal != nil:
			// Already failing; errors from cancelled siblings do not count.
		case httpx.IsAuthError(err) ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			fatal = err
			cancel()
		case ctx.Err() != nil:
			// The run is over; keep the context error, not a late failure.
			fatal = ctx.Err()
		default:
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
			record(fn(ctx, it))
		}()
	}
	wg.Wait()

	if fatal == nil {
		// The parent context may have ended without any item noticing.
		fatal = ctx.Err()
	}
	return metrics, itemErrors, fatal
}
