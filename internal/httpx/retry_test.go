package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/time/rate"
)

// step is one scripted answer of fakeTransport.
type step struct {
	code    int
	header  http.Header
	err     error
	bodyErr error  // when set, the body breaks with this error after a few bytes
	before  func() // when set, runs before the step answers
}

// brokenBody yields a truncated JSON document, then fails like a reset
// connection would.
type brokenBody struct {
	r   io.Reader
	err error
}

func (b *brokenBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}

// fakeTransport answers from a script without any socket, so that virtual
// time in a synctest bubble advances freely. The last step repeats.
type fakeTransport struct {
	mu    sync.Mutex
	steps []step
	times []time.Time
}

func (f *fakeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := min(len(f.times), len(f.steps)-1)
	f.times = append(f.times, time.Now())
	s := f.steps[i]
	if s.before != nil {
		s.before()
	}
	if s.err != nil {
		return nil, s.err
	}
	h := s.header
	if h == nil {
		h = http.Header{}
	}
	var body io.Reader = strings.NewReader(`{"ok":true}`)
	if s.bodyErr != nil {
		body = &brokenBody{r: strings.NewReader(`{"ok":`), err: s.bodyErr}
	}
	return &http.Response{
		StatusCode: s.code,
		Header:     h,
		Body:       io.NopCloser(body),
	}, nil
}

func (f *fakeTransport) requests() int { return len(f.times) }

// newTestClient must be called inside the synctest bubble. Jitter is zero.
func newTestClient(opts Options, steps ...step) (*Client, *fakeTransport) {
	ft := &fakeTransport{steps: steps}
	opts.BaseURL = "https://example.com"
	c := newClient(opts, ft)
	c.jitter = func() float64 { return 0 }
	return c, ft
}

func get(ctx context.Context, c *Client) error {
	var out any
	_, err := c.GetJSON(ctx, "/v1/x", nil, &out)
	return err
}

func TestRetryOn503ThenSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"}, step{code: 503}, step{code: 503}, step{code: 200})
		start := time.Now()
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if ft.requests() != 3 {
			t.Errorf("requests = %d, want 3", ft.requests())
		}
		if el := time.Since(start); el < 3*time.Second {
			t.Errorf("elapsed = %v, want >= 3s", el)
		}
	})
}

func TestRetryOnNetworkError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"}, step{err: errors.New("connection reset")}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if ft.requests() != 2 {
			t.Errorf("requests = %d, want 2", ft.requests())
		}
	})
}

func TestRetryOnTransportTimeout(t *testing.T) {
	// Since Go 1.23 transport timeouts match context.DeadlineExceeded; with
	// the caller's context alive they are network errors and are retried.
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"},
			step{err: fmt.Errorf("net/http: timeout awaiting response headers: %w", context.DeadlineExceeded)},
			step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if ft.requests() != 2 {
			t.Errorf("requests = %d, want 2", ft.requests())
		}
	})
}

func TestRetryOnBodyReadError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"},
			step{code: 200, bodyErr: errors.New("connection reset by peer")},
			step{code: 200})
		var out struct{ OK bool }
		if _, err := c.GetJSON(t.Context(), "/v1/x", nil, &out); err != nil {
			t.Fatal(err)
		}
		if !out.OK {
			t.Error("body of the second attempt was not decoded")
		}
		if ft.requests() != 2 {
			t.Errorf("requests = %d, want 2", ft.requests())
		}
	})
}

func TestNoRetryWhenCallerContextEnds(t *testing.T) {
	// The caller's own context ends while the request is in flight.
	t.Run("canceled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c, ft := newTestClient(Options{Provider: "p"}, step{err: context.Canceled, before: cancel})
			if err := get(ctx, c); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if ft.requests() != 1 {
				t.Errorf("requests = %d, want 1", ft.requests())
			}
		})
	})
	t.Run("deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			c, ft := newTestClient(Options{Provider: "p"},
				step{err: context.DeadlineExceeded, before: func() { time.Sleep(2 * time.Second) }})
			if err := get(ctx, c); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want context.DeadlineExceeded", err)
			}
			if ft.requests() != 1 {
				t.Errorf("requests = %d, want 1", ft.requests())
			}
		})
	})
}

func TestNoRetryOn401And404(t *testing.T) {
	for _, code := range []int{401, 404, 403, 500} {
		synctest.Test(t, func(t *testing.T) {
			c, ft := newTestClient(Options{Provider: "p"}, step{code: code})
			err := get(t.Context(), c)
			var se *StatusError
			if !errors.As(err, &se) || se.Code != code {
				t.Fatalf("err = %v", err)
			}
			if ft.requests() != 1 {
				t.Errorf("%d: requests = %d, want 1", code, ft.requests())
			}
		})
	}
}

func TestNoRetryOnNonRetryableErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		step step
		opts Options
		none bool // the request is never sent
	}{
		"dial":      {step: step{err: errDialBlocked}},
		"not GET":   {step: step{err: ErrMethodNotAllowed}},
		"bad JSON":  {step: step{code: 200, header: http.Header{}}},
		"auth fail": {step: step{code: 200}, opts: Options{Auth: func(*http.Request) error { return errors.New("no token") }}, none: true},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, ft := newTestClient(tc.opts, tc.step)
				var err error
				if name == "bad JSON" {
					var out chan int // cannot decode an object into a channel
					_, err = c.GetJSON(t.Context(), "/v1/x", nil, &out)
				} else {
					err = get(t.Context(), c)
				}
				if err == nil {
					t.Fatal("want error")
				}
				want := 1
				if tc.none {
					want = 0
				}
				if ft.requests() != want {
					t.Errorf("requests = %d, want %d", ft.requests(), want)
				}
			})
		})
	}
}

func TestRetryAfterSecondsAndDate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"},
			step{code: 429, header: http.Header{"Retry-After": {"7"}}}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if d := ft.times[1].Sub(ft.times[0]); d < 7*time.Second {
			t.Errorf("seconds form: gap = %v, want >= 7s", d)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		date := time.Now().Add(9 * time.Second).UTC().Format(http.TimeFormat)
		c, ft := newTestClient(Options{Provider: "p"},
			step{code: 429, header: http.Header{"Retry-After": {date}}}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if d := ft.times[1].Sub(ft.times[0]); d < 8*time.Second {
			t.Errorf("date form: gap = %v, want about 9s", d)
		}
	})
}

func TestRetryAfterHonouredOn503(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"},
			step{code: 503, header: http.Header{"Retry-After": {"5"}}}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if d := ft.times[1].Sub(ft.times[0]); d != 5*time.Second {
			t.Errorf("gap = %v, want 5s", d)
		}
	})
}

func TestRetryAfterCapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"},
			step{code: 429, header: http.Header{"Retry-After": {"3600"}}}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if d := ft.times[1].Sub(ft.times[0]); d != 30*time.Second {
			t.Errorf("gap = %v, want 30s", d)
		}
	})
}

func TestResetDelayUsedOn429WithoutRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p", ResetDelay: ResetSeconds},
			step{code: 429, header: http.Header{"X-Ratelimit-Reset": {"4"}}}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if d := ft.times[1].Sub(ft.times[0]); d != 4*time.Second {
			t.Errorf("gap = %v, want 4s", d)
		}
	})
	synctest.Test(t, func(t *testing.T) { // Retry-After wins over ResetDelay
		c, ft := newTestClient(Options{Provider: "p", ResetDelay: ResetSeconds},
			step{code: 429, header: http.Header{"Retry-After": {"2"}, "X-Ratelimit-Reset": {"4"}}}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if d := ft.times[1].Sub(ft.times[0]); d != 2*time.Second {
			t.Errorf("gap = %v, want 2s", d)
		}
	})
	synctest.Test(t, func(t *testing.T) { // ResetDelay is for 429 only
		c, ft := newTestClient(Options{Provider: "p", ResetDelay: ResetSeconds},
			step{code: 503, header: http.Header{"X-Ratelimit-Reset": {"4"}}}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if d := ft.times[1].Sub(ft.times[0]); d != time.Second {
			t.Errorf("gap = %v, want 1s backoff", d)
		}
	})
}

func TestResetSecondsAndUnix(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	hdr := func(v string) http.Header { return http.Header{"X-Ratelimit-Reset": {v}} }

	for _, tc := range []struct {
		name string
		fn   func(http.Header, time.Time) (time.Duration, bool)
		h    http.Header
		want time.Duration
		ok   bool
	}{
		{"seconds", ResetSeconds, hdr("12"), 12 * time.Second, true},
		{"seconds zero", ResetSeconds, hdr("0"), 0, true},
		{"seconds garbage", ResetSeconds, hdr("soon"), 0, false},
		{"seconds negative", ResetSeconds, hdr("-3"), 0, false},
		{"seconds missing", ResetSeconds, http.Header{}, 0, false},
		{"unix future", ResetUnix, hdr("1700000020"), 20 * time.Second, true},
		{"unix past", ResetUnix, hdr("1699999990"), 0, true},
		{"unix garbage", ResetUnix, hdr("0x10"), 0, false},
		{"unix negative", ResetUnix, hdr("-1"), 0, false},
		{"unix missing", ResetUnix, nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.fn(tc.h, now)
			if got != tc.want || ok != tc.ok {
				t.Errorf("got (%v, %v), want (%v, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRetryAfterParser(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		v    string
		want time.Duration
		ok   bool
	}{
		{"7", 7 * time.Second, true},
		{" 7 ", 7 * time.Second, true},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"-1", 0, false},
		{"garbage", 0, false},
		{"", 0, false},
	} {
		got, ok := retryAfter(http.Header{"Retry-After": {tc.v}}, now)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: got (%v, %v), want (%v, %v)", tc.v, got, ok, tc.want, tc.ok)
		}
	}
}

func TestGivesUpAfterThreeAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"}, step{code: 502})
		err := get(t.Context(), c)
		var se *StatusError
		if !errors.As(err, &se) || se.Code != 502 {
			t.Fatalf("err = %v", err)
		}
		if ft.requests() != 3 {
			t.Errorf("requests = %d, want 3", ft.requests())
		}
		if d := ft.times[2].Sub(ft.times[0]); d != 3*time.Second {
			t.Errorf("total wait = %v, want 3s (1s + 2s)", d)
		}
	})
}

func TestDeadlineShortCircuits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"},
			step{code: 429, header: http.Header{"Retry-After": {"10"}}}, step{code: 200})
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		start := time.Now()
		err := get(ctx, c)
		var se *StatusError
		if !errors.As(err, &se) || se.Code != 429 {
			t.Fatalf("err = %v, want the 429", err)
		}
		if ft.requests() != 1 {
			t.Errorf("requests = %d, want 1", ft.requests())
		}
		if el := time.Since(start); el != 0 {
			t.Errorf("returned after %v, want immediately", el)
		}
	})
}

func TestCancelDuringWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p"}, step{code: 503})
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(500*time.Millisecond, cancel)
		err := get(ctx, c)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if ft.requests() != 1 {
			t.Errorf("requests = %d, want 1", ft.requests())
		}
	})
}

func TestRateLimiterSpacesRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := newTestClient(Options{Provider: "p", RateLimit: rate.Limit(1), Burst: 1}, step{code: 200})
		start := time.Now()
		for range 3 {
			if err := get(t.Context(), c); err != nil {
				t.Fatal(err)
			}
		}
		if el := time.Since(start); el < 2*time.Second {
			t.Errorf("elapsed = %v, want >= 2s", el)
		}
	})
}

func TestLimiterErrorKeepsContextCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p", RateLimit: rate.Limit(1), Burst: 1}, step{code: 200})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := get(ctx, c)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled kept", err)
		}
		if !errors.Is(err, ErrPollDeadline) {
			t.Errorf("err = %v, want ErrPollDeadline", err)
		}
		if ft.requests() != 0 {
			t.Errorf("requests = %d, want 0", ft.requests())
		}
	})
}

func TestEveryAttemptIsCounted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		counter := NewRequestsCounter()
		c, _ := newTestClient(Options{Provider: "p", Requests: counter}, step{code: 503}, step{err: errors.New("boom")}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		for code, want := range map[string]float64{"503": 1, "error": 1, "200": 1} {
			if v := counterValue(counter, "p", code); v != want {
				t.Errorf("code %s = %v, want %v", code, v, want)
			}
		}
	})
}

func counterValue(cv *prometheus.CounterVec, labels ...string) float64 {
	return testutil.ToFloat64(cv.WithLabelValues(labels...))
}

func TestLimiterOutOfTimeFailsThePoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p", RateLimit: rate.Every(time.Minute), Burst: 1}, step{code: 200})
		if err := get(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		// The next token comes in a minute; the context ends before that.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		err := get(ctx, c)
		if !errors.Is(err, ErrPollDeadline) {
			t.Fatalf("err = %v, want ErrPollDeadline", err)
		}
		if ft.requests() != 1 {
			t.Errorf("requests = %d, want 1", ft.requests())
		}
	})
}

func TestLimiterRefusalOnRetryKeepsLastError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ft := newTestClient(Options{Provider: "p", RateLimit: rate.Every(10 * time.Second), Burst: 1}, step{code: 503})
		// The 1s backoff fits in the budget, the 9s wait for a token does not.
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		err := get(ctx, c)
		var se *StatusError
		if !errors.As(err, &se) || se.Code != 503 {
			t.Errorf("err = %v, want the 503 kept", err)
		}
		if !errors.Is(err, ErrPollDeadline) {
			t.Errorf("err = %v, want ErrPollDeadline", err)
		}
		if ft.requests() != 1 {
			t.Errorf("requests = %d, want 1", ft.requests())
		}
	})
}
