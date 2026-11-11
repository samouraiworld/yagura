package scaleway

import "github.com/prometheus/client_golang/prometheus"

// MetricNames lists the metric families this package exposes.
var MetricNames = []string{
	"yagura_scaleway_consumption_amount",
	"yagura_scaleway_consumption_updated_timestamp_seconds",
}

var (
	amountDesc = prometheus.NewDesc(MetricNames[0],
		"Month-to-date consumption per project and category, as reported by the Scaleway billing API.",
		[]string{"project_id", "project_name", "category", "currency"}, nil)
	updatedDesc = prometheus.NewDesc(MetricNames[1],
		"Unix time of the billing API's last consumption update.", nil, nil)
)
