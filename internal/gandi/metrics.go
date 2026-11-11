package gandi

import "github.com/prometheus/client_golang/prometheus"

// MetricNames lists the metric families this package exposes.
var MetricNames = []string{
	"yagura_gandi_domain_expiry_timestamp_seconds",
	"yagura_gandi_domain_autorenew",
}

var (
	expiryDesc = prometheus.NewDesc(MetricNames[0],
		"Unix time at which the domain registration ends.", []string{"domain"}, nil)
	autorenewDesc = prometheus.NewDesc(MetricNames[1],
		"Whether the domain renews automatically: 1 on, 0 off.", []string{"domain"}, nil)
)

func gauge(d *prometheus.Desc, v float64, domain string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, domain)
}
