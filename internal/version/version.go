// Package version exposes build metadata and the yagura_build_info metric.
package version

import (
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
)

// Version and Revision are set at build time with -ldflags -X.
var (
	Version  = "dev"
	Revision = "unknown"
)

// UserAgent returns the User-Agent header value sent on outbound requests.
func UserAgent() string {
	return "yagura/" + Version + " (+https://github.com/samouraiworld/yagura)"
}

// NewCollector returns a collector emitting yagura_build_info with value 1.
func NewCollector() prometheus.Collector {
	return prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "yagura_build_info",
		Help: "Build information about the yagura exporter; the value is always 1.",
		ConstLabels: prometheus.Labels{
			"version":   Version,
			"revision":  Revision,
			"goversion": runtime.Version(),
		},
	}, func() float64 { return 1 })
}
