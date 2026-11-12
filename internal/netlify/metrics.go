package netlify

import "github.com/prometheus/client_golang/prometheus"

// MetricNames lists the metric families this package exposes.
var MetricNames = []string{
	"yagura_netlify_last_deploy_info",
	"yagura_netlify_last_deploy_failed",
	"yagura_netlify_last_deploy_created_timestamp_seconds",
	"yagura_netlify_build_minutes_used",
	"yagura_netlify_build_minutes_included",
	"yagura_netlify_build_period_end_timestamp_seconds",
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
	buildUsedDesc = prometheus.NewDesc(MetricNames[3],
		"Build minutes used by the account in the current billing period.",
		[]string{"account"}, nil)
	buildIncludedDesc = prometheus.NewDesc(MetricNames[4],
		"Build minutes included in the plan of the account; absent when the plan has no numeric allowance.",
		[]string{"account"}, nil)
	buildPeriodEndDesc = prometheus.NewDesc(MetricNames[5],
		"End of the current build billing period of the account, in seconds since the Unix epoch.",
		[]string{"account"}, nil)
)

func gauge(d *prometheus.Desc, v float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
}
