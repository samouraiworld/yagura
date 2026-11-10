package httpx

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxAttempts  = 3
	maxWait      = 30 * time.Second
	baseBackoff  = time.Second
	jitterSpread = 0.2
)

// ResetSeconds reads X-RateLimit-Reset as a number of seconds until the
// limit resets (Supabase). Missing, negative or malformed values give false.
func ResetSeconds(h http.Header, _ time.Time) (time.Duration, bool) {
	n, ok := headerInt(h, "X-RateLimit-Reset")
	if !ok {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

// ResetUnix reads X-RateLimit-Reset as a Unix timestamp (Netlify). A time in
// the past gives a zero delay. Missing, negative or malformed values give false.
func ResetUnix(h http.Header, now time.Time) (time.Duration, bool) {
	n, ok := headerInt(h, "X-RateLimit-Reset")
	if !ok {
		return 0, false
	}
	return max(time.Unix(n, 0).Sub(now), 0), true
}

func headerInt(h http.Header, name string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(h.Get(name)), 10, 64)
	if err != nil || n < 0 || n > 1<<40 { // the bound keeps Duration math from overflowing
		return 0, false
	}
	return n, true
}

// retryAfter parses Retry-After, either delay-seconds or an HTTP date. A date
// in the past gives a zero delay.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 || n > 1<<40 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// defaultJitter returns a factor in [-0.2, +0.2]. It only desynchronises retries.
//
//nolint:gosec // G404: not a security use
func defaultJitter() float64 { return (rand.Float64()*2 - 1) * jitterSpread }

// retryableStatus reports the statuses worth another attempt.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryableError reports whether a transport error may be retried. Context
// errors and guard refusals are final.
func retryableError(ctx context.Context, err error) bool {
	return ctx.Err() == nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, errDialBlocked) &&
		!errors.Is(err, ErrMethodNotAllowed)
}

// waitBefore returns the pause before the next attempt. attempt is the
// zero-based index of the attempt that just failed; status is 0 for a
// transport error. Order: Retry-After, then (429 only) ResetDelay, then
// exponential backoff with jitter.
func (c *Client) waitBefore(attempt, status int, h http.Header, now time.Time) time.Duration {
	var d time.Duration
	if ra, ok := retryAfter(h, now); ok && status != 0 {
		d = ra
	} else if rd, ok := c.resetDelay(status, h, now); ok {
		d = rd
	} else {
		d = baseBackoff << attempt
		d += time.Duration(float64(d) * c.jitter())
	}
	return min(max(d, 0), maxWait)
}

func (c *Client) resetDelay(status int, h http.Header, now time.Time) (time.Duration, bool) {
	if status != http.StatusTooManyRequests || c.opts.ResetDelay == nil {
		return 0, false
	}
	return c.opts.ResetDelay(h, now)
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
