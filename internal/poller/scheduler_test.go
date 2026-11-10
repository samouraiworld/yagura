package poller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type step struct {
	res Result
	err error
}

// fakePoller replays a scripted list of results and records poll times. When
// the script is exhausted it repeats the last step. If block is set, Poll
// waits for ctx cancellation and records the ctx deadline.
type fakePoller struct {
	name     string
	interval time.Duration
	script   []step
	block    bool

	mu       sync.Mutex
	calls    []time.Time
	deadline time.Duration
}

func (f *fakePoller) Name() string            { return f.name }
func (f *fakePoller) Interval() time.Duration { return f.interval }

func (f *fakePoller) Poll(ctx context.Context) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, time.Now())
	n := len(f.calls)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		f.mu.Lock()
		f.deadline = time.Since(f.calls[0])
		f.mu.Unlock()
		return Result{}, ctx.Err()
	}
	i := min(n-1, len(f.script)-1)
	return f.script[i].res, f.script[i].err
}

func (f *fakePoller) callTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.calls...)
}

func newTestScheduler(ps ...Poller) *Scheduler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewScheduler(logger, func() time.Duration { return 0 }, ps...)
}

func ok(metrics ...prometheus.Metric) step { return step{res: Result{Metrics: metrics}} }

func fail() step { return step{err: errors.New("boom")} }

func named(t *testing.T, name string, v float64) prometheus.Metric {
	t.Helper()
	m, err := prometheus.NewConstMetric(
		prometheus.NewDesc(name, "test", nil, nil), prometheus.GaugeValue, v)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func runScheduler(s *Scheduler) (cancel func(), done <-chan struct{}) {
	ctx, c := context.WithCancel(context.Background())
	d := make(chan struct{})
	go func() { s.Run(ctx); close(d) }()
	return c, d
}

func expect(t *testing.T, s *Scheduler, text string, names ...string) {
	t.Helper()
	if err := testutil.CollectAndCompare(s, strings.NewReader(text), names...); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerNoSeriesBeforeFirstPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &fakePoller{name: "x", interval: time.Hour, block: true}
		s := newTestScheduler(p)
		cancel, done := runScheduler(s)
		defer func() { cancel(); <-done }()
		synctest.Wait()
		if n := testutil.CollectAndCount(s); n != 0 {
			t.Fatalf("got %d series before the first poll, want 0", n)
		}
		cancel()
		<-done
	})
}

func TestSchedulerSuccessThenFailureKeepsSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		p := &fakePoller{name: "x", interval: time.Hour,
			script: []step{ok(named(t, "m", 1)), fail()}}
		s := newTestScheduler(p)
		cancel, done := runScheduler(s)
		defer func() { cancel(); <-done }()
		synctest.Wait()

		ts := float64(start.Unix())
		expect(t, s, `# HELP m test
# TYPE m gauge
m 1
# HELP yagura_collector_up 1 if the last poll succeeded, else 0.
# TYPE yagura_collector_up gauge
yagura_collector_up{collector="x"} 1
# HELP yagura_collector_last_success_timestamp_seconds Unix time of the last successful poll.
# TYPE yagura_collector_last_success_timestamp_seconds gauge
yagura_collector_last_success_timestamp_seconds{collector="x"} `+formatFloat(ts)+`
# HELP yagura_collector_polls_total Poll attempts.
# TYPE yagura_collector_polls_total counter
yagura_collector_polls_total{collector="x",result="error"} 0
yagura_collector_polls_total{collector="x",result="success"} 1
`, "m", "yagura_collector_up", "yagura_collector_last_success_timestamp_seconds", "yagura_collector_polls_total")

		time.Sleep(time.Hour)
		synctest.Wait()
		expect(t, s, `# HELP m test
# TYPE m gauge
m 1
# HELP yagura_collector_up 1 if the last poll succeeded, else 0.
# TYPE yagura_collector_up gauge
yagura_collector_up{collector="x"} 0
# HELP yagura_collector_last_success_timestamp_seconds Unix time of the last successful poll.
# TYPE yagura_collector_last_success_timestamp_seconds gauge
yagura_collector_last_success_timestamp_seconds{collector="x"} `+formatFloat(ts)+`
# HELP yagura_collector_polls_total Poll attempts.
# TYPE yagura_collector_polls_total counter
yagura_collector_polls_total{collector="x",result="error"} 1
yagura_collector_polls_total{collector="x",result="success"} 1
`, "m", "yagura_collector_up", "yagura_collector_last_success_timestamp_seconds", "yagura_collector_polls_total")
		cancel()
		<-done
	})
}

func TestSchedulerReplacesSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &fakePoller{name: "x", interval: time.Hour, script: []step{
			ok(named(t, "a", 1), named(t, "b", 2)), ok(named(t, "a", 1))}}
		s := newTestScheduler(p)
		cancel, done := runScheduler(s)
		defer func() { cancel(); <-done }()
		synctest.Wait()
		expect(t, s, "# HELP a test\n# TYPE a gauge\na 1\n# HELP b test\n# TYPE b gauge\nb 2\n", "a", "b")
		time.Sleep(time.Hour)
		synctest.Wait()
		expect(t, s, "# HELP a test\n# TYPE a gauge\na 1\n", "a", "b")
		cancel()
		<-done
	})
}

func TestSchedulerRetryTiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		p := &fakePoller{name: "x", interval: 6 * time.Hour,
			script: []step{fail(), fail(), ok(named(t, "m", 1))}}
		s := newTestScheduler(p)
		cancel, done := runScheduler(s)
		defer func() { cancel(); <-done }()
		time.Sleep(3*time.Minute + 6*time.Hour + time.Second)
		synctest.Wait()
		cancel()
		<-done
		want := []time.Duration{0, time.Minute, 3 * time.Minute, 3*time.Minute + 6*time.Hour}
		got := p.callTimes()
		if len(got) != len(want) {
			t.Fatalf("got %d polls, want %d", len(got), len(want))
		}
		for i, w := range want {
			if d := got[i].Sub(start); d != w {
				t.Errorf("poll %d at %v, want %v", i, d, w)
			}
		}
	})
}

func TestSchedulerItemErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &fakePoller{name: "x", interval: time.Hour,
			script: []step{{res: Result{ItemErrors: 2}}}}
		s := newTestScheduler(p)
		cancel, done := runScheduler(s)
		defer func() { cancel(); <-done }()
		synctest.Wait()
		expect(t, s, `# HELP yagura_collector_last_item_errors Items failed or skipped in the last successful poll.
# TYPE yagura_collector_last_item_errors gauge
yagura_collector_last_item_errors{collector="x"} 2
`, "yagura_collector_last_item_errors")
		cancel()
		<-done
	})
}

func TestSchedulerStopsOnCancel(t *testing.T) {
	synctest.Test(t, func(_ *testing.T) {
		p := &fakePoller{name: "x", interval: time.Hour, script: []step{ok()}}
		s := newTestScheduler(p)
		cancel, done := runScheduler(s)
		defer func() { cancel(); <-done }()
		synctest.Wait()
		cancel()
		<-done
		synctest.Wait()
	})
}

func TestSchedulerStopsDuringJitter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &fakePoller{name: "x", interval: time.Hour, script: []step{ok()}}
		s := NewScheduler(slog.New(slog.NewTextHandler(io.Discard, nil)),
			func() time.Duration { return time.Hour }, p)
		cancel, done := runScheduler(s)
		defer func() { cancel(); <-done }()
		synctest.Wait()
		cancel()
		<-done
		if n := len(p.callTimes()); n != 0 {
			t.Fatalf("polled %d times, want 0", n)
		}
	})
}

func TestSchedulerPollTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &fakePoller{name: "x", interval: time.Minute, block: true}
		s := newTestScheduler(p)
		cancel, done := runScheduler(s)
		defer func() { cancel(); <-done }()
		time.Sleep(time.Minute)
		synctest.Wait()
		p.mu.Lock()
		got := p.deadline
		p.mu.Unlock()
		if got != 48*time.Second {
			t.Fatalf("poll ctx cancelled after %v, want 48s", got)
		}
		expect(t, s, `# HELP yagura_collector_polls_total Poll attempts.
# TYPE yagura_collector_polls_total counter
yagura_collector_polls_total{collector="x",result="error"} 1
yagura_collector_polls_total{collector="x",result="success"} 0
`, "yagura_collector_polls_total")
		cancel()
		<-done
	})
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
