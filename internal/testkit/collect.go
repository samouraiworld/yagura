package testkit

import "github.com/prometheus/client_golang/prometheus"

type staticCollector []prometheus.Metric

func (staticCollector) Describe(chan<- *prometheus.Desc) {} // unchecked collector

func (c staticCollector) Collect(ch chan<- prometheus.Metric) {
	for _, m := range c {
		ch <- m
	}
}

// Collector exposes a fixed metric slice, for testutil.CollectAndCompare.
func Collector(metrics []prometheus.Metric) prometheus.Collector {
	return staticCollector(metrics)
}
