package supabase

import "github.com/prometheus/client_golang/prometheus"

// MetricNames lists the metric families this package exposes.
var MetricNames = []string{
	"yagura_supabase_project_info",
	"yagura_supabase_project_status",
	"yagura_supabase_service_healthy",
}

var (
	infoDesc = prometheus.NewDesc(MetricNames[0],
		"Constant 1 carrying the name, organization and region of the project.",
		[]string{"project_ref", "project_name", "organization_slug", "region"}, nil)
	statusDesc = prometheus.NewDesc(MetricNames[1],
		"Project status as a state set: 1 for the current status, 0 for every other known one.",
		[]string{"project_ref", "status"}, nil)
	serviceHealthyDesc = prometheus.NewDesc(MetricNames[2],
		"Whether the service of the project is healthy: 1 healthy, 0 not.",
		[]string{"project_ref", "service"}, nil)
)

func gauge(d *prometheus.Desc, v float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
}
