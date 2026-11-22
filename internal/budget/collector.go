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
	// InBilling is false for a project with no consumption in the current
	// month: a manual mapping of an unseen project, or a project seen only
	// in the monthly history.
	InBilling bool `json:"in_billing"`
	// InHistory is true for a project of the stored monthly history, the
	// current month included.
	InHistory bool `json:"in_history"`
}

// Resolve applies the manual mappings, then the automatic rule, to the
// projects seen in the current month's billing. Manual mappings of unseen
// projects are listed too, with InBilling false. The result is sorted by
// business project, then project ID.
func Resolve(projects []scaleway.Project, mappings []Mapping) []Resolved {
	return ResolveWithHistory(projects, nil, mappings)
}

// ResolveWithHistory is Resolve with the projects of the monthly history:
// a history project absent from current is listed with InBilling false and
// its history name.
func ResolveWithHistory(current, history []scaleway.Project, mappings []Mapping) []Resolved {
	manual := make(map[string]string, len(mappings))
	for _, m := range mappings {
		manual[m.ProjectID] = m.Project
	}
	inHistory := make(map[string]bool, len(history))
	for _, p := range history {
		inHistory[strings.ToLower(p.ID)] = true
	}
	out := make([]Resolved, 0, len(current)+len(history)+len(mappings))
	seen := make(map[string]bool, len(current)+len(history))
	add := func(p scaleway.Project, inBilling bool) {
		id := strings.ToLower(p.ID)
		if seen[id] {
			return
		}
		seen[id] = true
		r := Resolved{ProjectID: p.ID, ProjectName: p.Name, InBilling: inBilling, InHistory: inHistory[id]}
		if m, ok := manual[id]; ok {
			r.Project, r.Mapping = m, MappingManual
		} else {
			r.Project, r.Mapping = AutoProject(p.Name, id), MappingAuto
		}
		out = append(out, r)
	}
	for _, p := range current {
		add(p, true)
	}
	for _, p := range history {
		add(p, false)
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
