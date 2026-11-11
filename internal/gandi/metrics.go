package gandi

import "github.com/prometheus/client_golang/prometheus"

// MetricNames lists the metric families this package exposes.
var MetricNames = []string{
	"yagura_gandi_domain_expiry_timestamp_seconds",
	"yagura_gandi_domain_autorenew",
	"yagura_gandi_prepaid_balance",
	"yagura_gandi_prepaid_warning_threshold",
}

var (
	expiryDesc = prometheus.NewDesc(MetricNames[0],
		"Unix time at which the domain registration ends.", []string{"domain"}, nil)
	autorenewDesc = prometheus.NewDesc(MetricNames[1],
		"Whether the domain renews automatically: 1 on, 0 off.", []string{"domain"}, nil)
	prepaidBalanceDesc = prometheus.NewDesc(MetricNames[2],
		"Prepaid account balance, in the currency of the label.", []string{"currency"}, nil)
	prepaidThresholdDesc = prometheus.NewDesc(MetricNames[3],
		"Prepaid balance below which Gandi sends a warning, in the currency of the label.", []string{"currency"}, nil)
)

func gauge(d *prometheus.Desc, v float64, label string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, label)
}
