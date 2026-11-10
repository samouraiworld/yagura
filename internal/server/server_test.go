package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samourai-crew/yagura/internal/version"
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
