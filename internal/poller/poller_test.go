package poller

import (
	"testing"
	"time"
)

func TestNextDelay(t *testing.T) {
	tests := []struct {
		interval time.Duration
		failures int
		want     time.Duration
	}{
		{time.Minute, 0, time.Minute},
		{time.Minute, 1, time.Minute},
		{time.Minute, 5, time.Minute},
		{6 * time.Hour, 0, 6 * time.Hour},
		{6 * time.Hour, 1, time.Minute},
		{6 * time.Hour, 2, 2 * time.Minute},
		{6 * time.Hour, 3, 4 * time.Minute},
		{6 * time.Hour, 6, 30 * time.Minute},
		{6 * time.Hour, 20, 30 * time.Minute},
		{6 * time.Hour, 1000, 30 * time.Minute},
		{5 * time.Minute, 4, 5 * time.Minute},
	}
	for _, tt := range tests {
		if got := NextDelay(tt.interval, tt.failures); got != tt.want {
			t.Errorf("NextDelay(%v, %d) = %v, want %v", tt.interval, tt.failures, got, tt.want)
		}
	}
}

func TestPollTimeout(t *testing.T) {
	tests := []struct{ interval, want time.Duration }{
		{time.Minute, 48 * time.Second},
		{5 * time.Minute, 4 * time.Minute},
		{6 * time.Hour, 10 * time.Minute},
	}
	for _, tt := range tests {
		if got := PollTimeout(tt.interval); got != tt.want {
			t.Errorf("PollTimeout(%v) = %v, want %v", tt.interval, got, tt.want)
		}
	}
}
