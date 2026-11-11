package supabase

import "github.com/prometheus/client_golang/prometheus"

// MetricNames lists the metric families this package exposes.
var MetricNames = []string{
	"yagura_supabase_project_info",
	"yagura_supabase_project_status",
	"yagura_supabase_service_healthy",
	"yagura_supabase_disk_size_bytes",
	"yagura_supabase_disk_used_bytes",
	"yagura_supabase_disk_available_bytes",
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
	diskSizeDesc = prometheus.NewDesc(MetricNames[3],
		"Size of the disk of the project in bytes.",
		[]string{"project_ref"}, nil)
	diskUsedDesc = prometheus.NewDesc(MetricNames[4],
		"Used space on the disk of the project in bytes.",
		[]string{"project_ref"}, nil)
	diskAvailableDesc = prometheus.NewDesc(MetricNames[5],
		"Available space on the disk of the project in bytes.",
		[]string{"project_ref"}, nil)
)

func gauge(d *prometheus.Desc, v float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
}
