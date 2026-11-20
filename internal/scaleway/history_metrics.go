package scaleway

import "github.com/prometheus/client_golang/prometheus"

// HistoryMetricNames lists the metric families the history collector
// exposes.
var HistoryMetricNames = []string{
	"yagura_scaleway_monthly_consumption_amount",
	"yagura_scaleway_invoice_amount",
	"yagura_scaleway_monthly_last_success_timestamp_seconds",
}

var (
	monthlyAmountDesc = prometheus.NewDesc(HistoryMetricNames[0],
		"Consumption per billing period (UTC calendar month), project and category, excluding VAT and before discounts, as reported by the Scaleway billing API; the current month and the last closed months.",
		[]string{"billing_period", "project_id", "project_name", "project", "category", "currency"}, nil)
	invoiceAmountDesc = prometheus.NewDesc(HistoryMetricNames[1],
		"Sum over a closed billing period's Scaleway invoices of total_untaxed (kind=untaxed, after discount) or total_discount (kind=discount, negative).",
		[]string{"billing_period", "kind", "currency"}, nil)
	monthlySuccessDesc = prometheus.NewDesc(HistoryMetricNames[2],
		"Unix time of the last successful read of a billing period's consumption.",
		[]string{"billing_period"}, nil)
)

// ProjectMapper returns the business project of each listed project, by ID.
type ProjectMapper func(projects []Project) map[string]string

// HistoryCollector exposes the history at scrape time, so that a changed
// mapping applies to every month at the next scrape.
type HistoryCollector struct {
	history *History
	mapper  ProjectMapper
}

// NewHistoryCollector returns the collector of history. A project absent
// from the mapper's answer, or every project when mapper is nil, gets its
// project ID as business project.
func NewHistoryCollector(history *History, mapper ProjectMapper) *HistoryCollector {
	return &HistoryCollector{history: history, mapper: mapper}
}

// Describe implements prometheus.Collector.
func (c *HistoryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- monthlyAmountDesc
	ch <- invoiceAmountDesc
	ch <- monthlySuccessDesc
}

// Collect implements prometheus.Collector. A month read with success but
// empty has a last-success series and no amount series.
func (c *HistoryCollector) Collect(ch chan<- prometheus.Metric) {
	v := c.history.view(c.history.now())
	projects := make([]Project, 0, len(v.names))
	for id, name := range v.names {
		projects = append(projects, Project{ID: id, Name: name})
	}
	var business map[string]string
	if c.mapper != nil {
		business = c.mapper(projects)
	}
	for _, m := range v.months {
		for _, l := range m.Lines {
			project, ok := business[l.ProjectID]
			if !ok {
				project = l.ProjectID
			}
			ch <- prometheus.MustNewConstMetric(monthlyAmountDesc, prometheus.GaugeValue, l.Amount,
				m.BillingPeriod, l.ProjectID, v.names[l.ProjectID], project, l.Category, l.Currency)
		}
		ch <- prometheus.MustNewConstMetric(monthlySuccessDesc, prometheus.GaugeValue,
			float64(m.LastSuccess.UnixNano())/1e9, m.BillingPeriod)
	}
	for _, a := range v.invoices {
		ch <- prometheus.MustNewConstMetric(invoiceAmountDesc, prometheus.GaugeValue, a.Amount,
			a.BillingPeriod, a.Kind, a.Currency)
	}
}
