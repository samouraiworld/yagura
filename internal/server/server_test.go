package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/version"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Code, string(body)
}

func handler(t *testing.T, reg *prometheus.Registry) http.Handler {
	t.Helper()
	return Handler(Options{Gatherer: reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func TestHealthz(t *testing.T) {
	code, body := get(t, handler(t, prometheus.NewRegistry()), "/healthz")
	if code != http.StatusOK || strings.TrimSpace(body) != "ok" {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestMetricsServesBuildInfo(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(version.NewCollector())
	code, body := get(t, handler(t, reg), "/metrics")
	if code != http.StatusOK || !strings.Contains(body, "yagura_build_info") {
		t.Fatalf("got %d %q", code, body)
	}
}

// dupCollector is an unchecked collector emitting the same series twice
// plus one valid gauge.
type dupCollector struct{}

func (dupCollector) Describe(chan<- *prometheus.Desc) {}

func (dupCollector) Collect(ch chan<- prometheus.Metric) {
	d := prometheus.NewDesc("demo_dup", "duplicate series", nil, nil)
	ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, 2)
	ok := prometheus.NewDesc("demo_valid", "valid gauge", nil, nil)
	ch <- prometheus.MustNewConstMetric(ok, prometheus.GaugeValue, 3)
}

func TestMetricsContinueOnCollectError(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(dupCollector{})
	code, body := get(t, handler(t, reg), "/metrics")
	if code != http.StatusOK || !strings.Contains(body, "demo_valid 3") {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestLandingPage(t *testing.T) {
	code, body := get(t, handler(t, prometheus.NewRegistry()), "/")
	if code != http.StatusOK || !strings.Contains(body, "/metrics") {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestLandingPageHasNoProfilingLinks(t *testing.T) {
	// /debug/pprof is not served, and the page must not point elsewhere.
	// The toolkit's built-in CSS always holds an inert "#pprof" rule, so the
	// check looks for the profiling block and its links, not the bare word.
	_, body := get(t, handler(t, prometheus.NewRegistry()), "/")
	for _, bad := range []string{`id="pprof"`, "debug/pprof", "pprof.me"} {
		if strings.Contains(body, bad) {
			t.Errorf("landing page contains %q", bad)
		}
	}
}

func TestServerTimeouts(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler())
	for name, tc := range map[string]struct{ got, want time.Duration }{
		"ReadHeaderTimeout": {srv.ReadHeaderTimeout, 10 * time.Second},
		"ReadTimeout":       {srv.ReadTimeout, 30 * time.Second},
		"WriteTimeout":      {srv.WriteTimeout, 30 * time.Second},
		"IdleTimeout":       {srv.IdleTimeout, 60 * time.Second},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", name, tc.got, tc.want)
		}
	}
}
