// Package reference is the single list of metric names yagura exports. The
// README and dashboard tests check themselves against it.
package reference

import (
	"slices"

	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/statuspage"
)

// MetricNames returns the sorted, de-duplicated names of every metric yagura
// exports. Each provider package appends its own MetricNames here.
func MetricNames() []string {
	var names []string
	names = append(names, poller.SelfMetricNames...)
	names = append(names, "yagura_api_requests_total", "yagura_build_info")
	// Providers: append one `xxx.MetricNames...` line per provider package.
	names = append(names, statuspage.MetricNames...)
	slices.Sort(names)
	return slices.Compact(names)
}
