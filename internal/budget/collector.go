package budget

import (
	"cmp"
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/scaleway"
)

// Mapping kinds.
const (
	MappingAuto   = "auto"
	MappingManual = "manual"
)

// MetricNames lists the metric families this package exposes.
var MetricNames = []string{
	"yagura_budget_amount",
	"yagura_scaleway_project_info",
}

var (
	amountDesc = prometheus.NewDesc(MetricNames[0],
		"Monthly budget excluding VAT, per scope (total, scaleway, project) and business project, as stored by yagura.",
		[]string{"scope", "project", "currency"}, nil)
	projectInfoDesc = prometheus.NewDesc(MetricNames[1],
		"Business project of each Scaleway project seen in the current month's billing data, always 1; mapping is auto or manual.",
		[]string{"project_id", "project_name", "project", "mapping"}, nil)
)

// Resolved is a Scaleway project with its business project.
type Resolved struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	Project     string `json:"project"`
	Mapping     string `json:"mapping"`
	// InBilling is false for a manual mapping whose project has no
	// consumption in the current month.
	InBilling bool `json:"in_billing"`
}

// Resolve applies the manual mappings, then the automatic rule, to the
// projects seen in billing. Manual mappings of unseen projects are listed
// too, with InBilling false. The result is sorted by business project, then
// project ID.
func Resolve(projects []scaleway.Project, mappings []Mapping) []Resolved {
	manual := make(map[string]string, len(mappings))
	for _, m := range mappings {
		manual[m.ProjectID] = m.Project
	}
	out := make([]Resolved, 0, len(projects)+len(mappings))
	seen := make(map[string]bool, len(projects))
	for _, p := range projects {
		id := strings.ToLower(p.ID)
		seen[id] = true
		r := Resolved{ProjectID: p.ID, ProjectName: p.Name, InBilling: true}
		if m, ok := manual[id]; ok {
			r.Project, r.Mapping = m, MappingManual
		} else {
			r.Project, r.Mapping = AutoProject(p.Name, id), MappingAuto
		}
		out = append(out, r)
	}
	for _, m := range mappings {
		if !seen[m.ProjectID] {
			out = append(out, Resolved{ProjectID: m.ProjectID, Project: m.Project, Mapping: MappingManual})
		}
	}
	slices.SortFunc(out, func(a, b Resolved) int {
		return cmp.Or(cmp.Compare(a.Project, b.Project), cmp.Compare(a.ProjectID, b.ProjectID))
	})
	return out
}

// HasScalewayProject reports whether a project seen in billing maps to the
// business project.
func HasScalewayProject(resolved []Resolved, project string) bool {
	return slices.ContainsFunc(resolved, func(r Resolved) bool { return r.InBilling && r.Project == project })
}

// Collector exposes the budgets and the project mapping at scrape time, so a
// change shows at the next scrape.
type Collector struct {
	store    *Store
	projects func() []scaleway.Project
}

// NewCollector returns a collector of the store's budgets and of the
// mapping of the listed projects. Either may be nil.
func NewCollector(store *Store, projects func() []scaleway.Project) *Collector {
	return &Collector{store: store, projects: projects}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- amountDesc
	ch <- projectInfoDesc
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	var mappings []Mapping
	if c.store != nil {
		for _, b := range c.store.Budgets() {
			ch <- prometheus.MustNewConstMetric(amountDesc, prometheus.GaugeValue, b.Amount, b.Scope, b.Project, b.Currency)
		}
		mappings = c.store.Mappings()
	}
	if c.projects == nil {
		return
	}
	for _, r := range Resolve(c.projects(), mappings) {
		if r.InBilling {
			ch <- prometheus.MustNewConstMetric(projectInfoDesc, prometheus.GaugeValue, 1, r.ProjectID, r.ProjectName, r.Project, r.Mapping)
		}
	}
}
