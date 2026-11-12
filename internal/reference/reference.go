// Package reference is the single list of metric names yagura exports. The
// README and dashboard tests check themselves against it.
package reference

import (
	"slices"

	"github.com/samouraiworld/yagura/internal/gandi"
	"github.com/samouraiworld/yagura/internal/netlify"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/scaleway"
	"github.com/samouraiworld/yagura/internal/statuspage"
	"github.com/samouraiworld/yagura/internal/supabase"
)

// MetricNames returns the sorted, de-duplicated names of every metric yagura
// exports. Each provider package appends its own MetricNames here.
func MetricNames() []string {
	var names []string
	names = append(names, poller.SelfMetricNames...)
	names = append(names, "yagura_api_requests_total", "yagura_build_info")
	// Providers: append one `xxx.MetricNames...` line per provider package.
	names = append(names, statuspage.MetricNames...)
	names = append(names, gandi.MetricNames...)
	names = append(names, scaleway.MetricNames...)
	names = append(names, supabase.MetricNames...)
	names = append(names, netlify.MetricNames...)
	slices.Sort(names)
	return slices.Compact(names)
}
