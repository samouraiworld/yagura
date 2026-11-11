package statuspage

import "github.com/prometheus/client_golang/prometheus"

// MetricNames lists the metric families this package exposes.
var MetricNames = []string{
	"yagura_statuspage_indicator",
	"yagura_statuspage_unresolved_incidents",
	"yagura_statuspage_active_maintenances",
	"yagura_statuspage_updated_timestamp_seconds",
}

var (
	indicatorDesc = prometheus.NewDesc(MetricNames[0],
		"Page status indicator: none=0, minor=1, major=2, critical=3, maintenance=-2, unknown=-1.", []string{"provider"}, nil)
	incidentsDesc = prometheus.NewDesc(MetricNames[1],
		"Number of unresolved incidents.", []string{"provider"}, nil)
	maintenancesDesc = prometheus.NewDesc(MetricNames[2],
		"Number of active scheduled maintenances.", []string{"provider"}, nil)
	updatedDesc = prometheus.NewDesc(MetricNames[3],
		"Unix time of the page's last update.", []string{"provider"}, nil)
)

func gauge(d *prometheus.Desc, v float64, provider string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, provider)
}
