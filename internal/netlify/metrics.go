package netlify

import "github.com/prometheus/client_golang/prometheus"

// MetricNames lists the metric families this package exposes.
var MetricNames = []string{
	"yagura_netlify_last_deploy_info",
	"yagura_netlify_last_deploy_failed",
	"yagura_netlify_last_deploy_created_timestamp_seconds",
}

var (
	deployInfoDesc = prometheus.NewDesc(MetricNames[0],
		"Constant 1 carrying the raw state of the last production deploy of the site.",
		[]string{"site", "account", "state"}, nil)
	deployFailedDesc = prometheus.NewDesc(MetricNames[1],
		"Whether the last production deploy of the site failed: 1 if its state is error, 0 otherwise.",
		[]string{"site"}, nil)
	deployCreatedDesc = prometheus.NewDesc(MetricNames[2],
		"Creation time of the last production deploy of the site, in seconds since the Unix epoch.",
		[]string{"site"}, nil)
)

func gauge(d *prometheus.Desc, v float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
}
