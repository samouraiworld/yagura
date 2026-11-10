// Package poller defines the contract every provider poller implements and
// the helpers shared by the scheduler and the pollers.
package poller

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	maxPollTimeout = 10 * time.Minute
	baseRetryDelay = time.Minute
	maxRetryDelay  = 30 * time.Minute
)

// Result is what a successful poll returns: the complete metric set and the
// number of items that failed or were skipped (yagura_collector_last_item_errors).
type Result struct {
	Metrics    []prometheus.Metric
	ItemErrors int
}

// Poller is one unit of background work with its own interval.
type Poller interface {
	// Name is the value of the collector label.
	Name() string
	Interval() time.Duration
	// Poll builds the complete metric set. A non-nil error fails the poll and
	// the scheduler keeps the previous snapshot.
	Poll(ctx context.Context) (Result, error)
}

// NextDelay returns how long to wait before the next poll. With no failure it
// is the interval; after n consecutive failures it is 1m*2^(n-1), capped at
// min(interval, 30m).
func NextDelay(interval time.Duration, failures int) time.Duration {
	if failures <= 0 {
		return interval
	}
	limit := min(interval, maxRetryDelay)
	d := baseRetryDelay
	for i := 1; i < failures && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// PollTimeout returns the context timeout of one poll: min(0.8*interval, 10m).
func PollTimeout(interval time.Duration) time.Duration {
	return min(interval/5*4, maxPollTimeout)
}
