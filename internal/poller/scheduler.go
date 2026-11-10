package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const maxStartJitter = 5 * time.Second

// SelfMetricNames lists the self-monitoring metrics the Scheduler exposes.
var SelfMetricNames = []string{
	"yagura_collector_up",
	"yagura_collector_last_success_timestamp_seconds",
	"yagura_collector_last_duration_seconds",
	"yagura_collector_last_item_errors",
	"yagura_collector_polls_total",
}

var (
	upDesc = prometheus.NewDesc("yagura_collector_up",
		"1 if the last poll succeeded, else 0.", []string{"collector"}, nil)
	lastSuccessDesc = prometheus.NewDesc("yagura_collector_last_success_timestamp_seconds",
		"Unix time of the last successful poll.", []string{"collector"}, nil)
	durationDesc = prometheus.NewDesc("yagura_collector_last_duration_seconds",
		"Duration of the last poll.", []string{"collector"}, nil)
	itemErrorsDesc = prometheus.NewDesc("yagura_collector_last_item_errors",
		"Items failed or skipped in the last successful poll.", []string{"collector"}, nil)
	pollsDesc = prometheus.NewDesc("yagura_collector_polls_total",
		"Poll attempts.", []string{"collector", "result"}, nil)
)

// state is what the scheduler remembers about one poller. It exists only
// after the first poll has completed.
type state struct {
	snapshot    []prometheus.Metric
	up          bool
	hasSuccess  bool
	lastSuccess time.Time
	duration    time.Duration
	itemErrors  int
	successes   uint64
	errors      uint64
}

// Scheduler runs pollers in the background and is the single prometheus
// collector that serves their snapshots plus the self-monitoring metrics.
type Scheduler struct {
	logger  *slog.Logger
	jitter  func() time.Duration
	pollers []Poller

	mu     sync.Mutex
	states map[string]*state
}

// NewScheduler builds a scheduler. A nil jitter draws a uniform start delay
// in [0, 5s).
func NewScheduler(logger *slog.Logger, jitter func() time.Duration, pollers ...Poller) *Scheduler {
	if jitter == nil {
		jitter = defaultJitter
	}
	return &Scheduler{
		logger:  logger,
		jitter:  jitter,
		pollers: pollers,
		states:  make(map[string]*state, len(pollers)),
	}
}

func defaultJitter() time.Duration {
	//nolint:gosec // G404: spreading start times is not a security use
	return time.Duration(rand.Int64N(int64(maxStartJitter)))
}

// Run starts one goroutine per poller and returns once all have exited,
// which happens after ctx is done.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, p := range s.pollers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.loop(ctx, p)
		}()
	}
	wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context, p Poller) {
	if !sleep(ctx, s.jitter()) {
		return
	}
	failures := 0
	for {
		if s.pollOnce(ctx, p) {
			failures = 0
		} else {
			failures++
		}
		if !sleep(ctx, NextDelay(p.Interval(), failures)) {
			return
		}
	}
}

// pollOnce runs one poll and records its outcome. It reports whether the
// poll succeeded. A poll cut short by shutdown is not recorded.
func (s *Scheduler) pollOnce(ctx context.Context, p Poller) bool {
	pctx, cancel := context.WithTimeout(ctx, PollTimeout(p.Interval()))
	defer cancel()
	start := time.Now()
	res, err := callPoll(pctx, p)
	end := time.Now()
	pe, panicked := errors.AsType[*panicError](err)
	if panicked {
		s.logger.Error("poll panicked", slog.String("collector", p.Name()), slog.Any("error", err),
			slog.String("stack", string(pe.stack)))
	}
	if ctx.Err() != nil {
		return err == nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[p.Name()]
	if st == nil {
		st = &state{}
		s.states[p.Name()] = st
	}
	st.duration = end.Sub(start)
	if err != nil {
		st.up = false
		st.errors++
		if !panicked {
			s.logger.Warn("poll failed", slog.String("collector", p.Name()), slog.Any("error", err))
		}
		return false
	}
	st.up = true
	st.hasSuccess = true
	st.lastSuccess = end
	st.snapshot = res.Metrics
	st.itemErrors = res.ItemErrors
	st.successes++
	return true
}

// callPoll runs p.Poll and turns a panic into a poll error, so that one
// broken poller fails its own poll instead of crashing the exporter.
func callPoll(ctx context.Context, p Poller) (res Result, err error) {
	defer func() {
		if v := recover(); v != nil {
			res, err = Result{}, &panicError{msg: fmt.Sprintf("poller %s panicked: %v", p.Name(), v), stack: debug.Stack()}
		}
	}()
	return p.Poll(ctx)
}

// sleep waits for d or for ctx to be done. It reports whether the full
// delay elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Describe sends nothing: the scheduler is an unchecked collector because
// its metric set changes as pollers produce snapshots.
func (s *Scheduler) Describe(chan<- *prometheus.Desc) {}

// Collect emits every snapshot and the self-monitoring series. It copies
// state under the lock and sends after releasing it.
func (s *Scheduler) Collect(ch chan<- prometheus.Metric) {
	type entry struct {
		name string
		st   state
	}
	s.mu.Lock()
	entries := make([]entry, 0, len(s.states))
	for _, p := range s.pollers {
		if st, ok := s.states[p.Name()]; ok {
			entries = append(entries, entry{p.Name(), *st})
		}
	}
	s.mu.Unlock()

	for _, e := range entries {
		st := e.st
		for _, m := range st.snapshot {
			ch <- m
		}
		up := 0.0
		if st.up {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(upDesc, prometheus.GaugeValue, up, e.name)
		if st.hasSuccess {
			ch <- prometheus.MustNewConstMetric(lastSuccessDesc, prometheus.GaugeValue,
				float64(st.lastSuccess.UnixNano())/1e9, e.name)
			ch <- prometheus.MustNewConstMetric(itemErrorsDesc, prometheus.GaugeValue,
				float64(st.itemErrors), e.name)
		}
		ch <- prometheus.MustNewConstMetric(durationDesc, prometheus.GaugeValue,
			st.duration.Seconds(), e.name)
		ch <- prometheus.MustNewConstMetric(pollsDesc, prometheus.CounterValue,
			float64(st.successes), e.name, "success")
		ch <- prometheus.MustNewConstMetric(pollsDesc, prometheus.CounterValue,
			float64(st.errors), e.name, "error")
	}
}
