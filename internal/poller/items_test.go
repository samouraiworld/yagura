package poller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samourai-crew/yagura/internal/httpx"
)

func metric(t *testing.T, name string) prometheus.Metric {
	t.Helper()
	m, err := prometheus.NewConstMetric(
		prometheus.NewDesc(name, "test", nil, nil), prometheus.GaugeValue, 1)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunItemsClassification(t *testing.T) {
	items := []string{"ok", "gone", "broken", "partial"}
	fn := func(_ context.Context, it string) ([]prometheus.Metric, error) {
		switch it {
		case "ok":
			return []prometheus.Metric{metric(t, "ok")}, nil
		case "gone":
			return nil, &httpx.StatusError{Path: "/gone", Code: 404}
		case "broken":
			return nil, &httpx.StatusError{Path: "/broken", Code: 500}
		default:
			return []prometheus.Metric{metric(t, "partial")}, &httpx.StatusError{Path: "/partial", Code: 500}
		}
	}
	ms, itemErrors, err := RunItems(context.Background(), items, fn)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(ms) != 2 {
		t.Errorf("got %d metrics, want 2", len(ms))
	}
	if itemErrors != 2 {
		t.Errorf("itemErrors = %d, want 2", itemErrors)
	}
}

func TestRunItemsAuthFails(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6, 7, 8}
	var started atomic.Int32
	fn := func(ctx context.Context, it int) ([]prometheus.Metric, error) {
		started.Add(1)
		if it == 1 {
			return nil, &httpx.StatusError{Path: "/x", Code: 403}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, _, err := RunItems(context.Background(), items, fn)
	if !httpx.IsAuthError(err) {
		t.Fatalf("err = %v, want auth error", err)
	}
	if n := started.Load(); n > 4 {
		t.Errorf("%d items started, want at most 4 (no new items after auth failure)", n)
	}
}

func TestRunItemsContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	fn := func(ctx context.Context, _ int) ([]prometheus.Metric, error) {
		if calls.Add(1) == 1 {
			cancel()
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, itemErrors, err := RunItems(ctx, make([]int, 50), fn)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if itemErrors != 0 {
		t.Errorf("itemErrors = %d, want 0 (context errors are not item errors)", itemErrors)
	}
	if n := calls.Load(); n > 4 {
		t.Errorf("%d items started after cancel, want at most 4", n)
	}
}

func TestRunItemsConcurrencyBound(t *testing.T) {
	const limit = 4
	var (
		mu       sync.Mutex
		inFlight int
		maxSeen  int
		reached  = make(chan struct{})
		once     sync.Once
	)
	fn := func(_ context.Context, _ int) ([]prometheus.Metric, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		if inFlight == limit {
			once.Do(func() { close(reached) })
		}
		mu.Unlock()

		// Barrier: wait until 4 items are in flight at once. A serial
		// runner never gets there and times out instead of hanging.
		select {
		case <-reached:
		case <-time.After(5 * time.Second):
			t.Error("timed out waiting for 4 items in flight")
		}

		mu.Lock()
		inFlight--
		mu.Unlock()
		return nil, nil
	}
	if _, _, err := RunItems(context.Background(), make([]int, 20), fn); err != nil {
		t.Fatal(err)
	}
	if maxSeen != limit {
		t.Errorf("max in flight = %d, want exactly %d", maxSeen, limit)
	}
}

func TestRunItemsItemTimeoutIsItemError(t *testing.T) {
	// Since Go 1.23 transport timeouts match context.DeadlineExceeded. While
	// the run context is alive such an error belongs to one item only.
	items := []string{"slow", "ok"}
	fn := func(_ context.Context, it string) ([]prometheus.Metric, error) {
		if it == "slow" {
			return nil, fmt.Errorf("GET /slow: %w", context.DeadlineExceeded)
		}
		return []prometheus.Metric{metric(t, "ok")}, nil
	}
	ms, itemErrors, err := RunItems(context.Background(), items, fn)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if itemErrors != 1 {
		t.Errorf("itemErrors = %d, want 1", itemErrors)
	}
	if len(ms) != 1 {
		t.Errorf("got %d metrics, want 1", len(ms))
	}
}

func TestRunItemsPanicFailsThePoll(t *testing.T) {
	// A panic on a worker goroutine is out of reach of the scheduler's
	// recover: RunItems must turn it into a fatal error, not crash the process.
	items := []int{1, 2, 3, 4, 5, 6, 7, 8}
	var started, finished atomic.Int32
	fn := func(ctx context.Context, it int) ([]prometheus.Metric, error) {
		started.Add(1)
		defer finished.Add(1)
		if it == 1 {
			panic("kaboom")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, itemErrors, err := RunItems(context.Background(), items, fn)
	if err == nil || !strings.Contains(err.Error(), "item panicked: kaboom") {
		t.Fatalf("err = %v, want the item panic", err)
	}
	if itemErrors != 0 {
		t.Errorf("itemErrors = %d, want 0", itemErrors)
	}
	if s, f := started.Load(), finished.Load(); s != f {
		t.Errorf("%d items started, %d finished: workers leaked", s, f)
	}
	if n := started.Load(); n > 4 {
		t.Errorf("%d items started, want at most 4 (no new items after the panic)", n)
	}
}

func TestRunItemsPollDeadlineFails(t *testing.T) {
	// The rate limiter refusing for lack of time must fail the poll, not
	// replace a full snapshot with a partial one.
	items := []string{"ok", "late"}
	fn := func(_ context.Context, it string) ([]prometheus.Metric, error) {
		if it == "late" {
			return nil, fmt.Errorf("GET /late: %w", httpx.ErrPollDeadline)
		}
		return []prometheus.Metric{metric(t, "ok")}, nil
	}
	_, itemErrors, err := RunItems(context.Background(), items, fn)
	if !errors.Is(err, httpx.ErrPollDeadline) {
		t.Fatalf("err = %v, want ErrPollDeadline", err)
	}
	if itemErrors != 0 {
		t.Errorf("itemErrors = %d, want 0", itemErrors)
	}
}
