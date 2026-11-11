// Package scaleway polls the Scaleway billing API.
package scaleway

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
)

const (
	consumptionsPath = "/billing/v2beta1/consumptions"
	pageSize         = 100
	nanosPerUnit     = 1_000_000_000
)

type consumptionPoller struct {
	client         *httpx.Client
	organizationID string
	interval       time.Duration
	logger         *slog.Logger
}

// NewConsumptionPoller returns the poller of the organization's month-to-date
// consumption, named "scaleway_consumption".
func NewConsumptionPoller(client *httpx.Client, organizationID string, interval time.Duration, logger *slog.Logger) poller.Poller {
	return &consumptionPoller{client: client, organizationID: organizationID, interval: interval, logger: logger}
}

func (p *consumptionPoller) Name() string            { return "scaleway_consumption" }
func (p *consumptionPoller) Interval() time.Duration { return p.interval }

type money struct {
	CurrencyCode string `json:"currency_code"`
	Units        int64  `json:"units"`
	Nanos        int32  `json:"nanos"`
}

type consumptionRow struct {
	Value        *money `json:"value"`
	ProjectID    string `json:"project_id"`
	ProjectName  string `json:"project_name"`
	CategoryName string `json:"category_name"`
}

type consumptionsPage struct {
	Consumptions []consumptionRow `json:"consumptions"` // nil when absent or null
	TotalCount   *int             `json:"total_count"`
	UpdatedAt    string           `json:"updated_at"`
}

// groupKey is the label set of one consumption series. SKU, product and
// resource never become labels (cardinality).
type groupKey struct {
	projectID, projectName, category, currency string
}

// total sums money values exactly; nanos may grow past one unit.
type total struct {
	units, nanos int64
}

func (t total) amount() float64 {
	units := t.units + t.nanos/nanosPerUnit
	nanos := int32(t.nanos % nanosPerUnit) //nolint:gosec // |x % 1e9| < 1e9 fits in int32
	return moneyAmount(units, nanos)
}

// moneyAmount converts a Scaleway money value to a float.
func moneyAmount(units int64, nanos int32) float64 {
	return float64(units) + float64(nanos)/1e9
}

// Poll reads every page of the current month's consumption (billing_period
// omitted) and sums the rows per project, category and currency. It stops
// once total_count rows are read or on an empty page; more than
// httpx.MaxPages pages fail the poll.
func (p *consumptionPoller) Poll(ctx context.Context) (poller.Result, error) {
	totals := map[groupKey]total{}
	var updatedAt string
	var itemErrors, read int
	for page := 1; page <= httpx.MaxPages; page++ {
		query := url.Values{
			"organization_id": {p.organizationID},
			"page":            {strconv.Itoa(page)},
			"page_size":       {strconv.Itoa(pageSize)},
		}
		var resp consumptionsPage
		if _, err := p.client.GetJSON(ctx, consumptionsPath, query, &resp); err != nil {
			return poller.Result{}, fmt.Errorf("scaleway consumption: page %d: %w", page, err)
		}
		if resp.Consumptions == nil {
			return poller.Result{}, fmt.Errorf("scaleway consumption: page %d: missing \"consumptions\" array", page)
		}
		if page == 1 {
			updatedAt = resp.UpdatedAt
		}
		for _, row := range resp.Consumptions {
			if row.Value == nil || row.Value.CurrencyCode == "" {
				p.logger.Warn("scaleway consumption row has no value or currency; row skipped",
					"project_id", capped(row.ProjectID), "category", capped(row.CategoryName))
				itemErrors++
				continue
			}
			k := groupKey{row.ProjectID, row.ProjectName, row.CategoryName, row.Value.CurrencyCode}
			t := totals[k]
			t.units += row.Value.Units
			t.nanos += int64(row.Value.Nanos)
			totals[k] = t
		}
		read += len(resp.Consumptions)
		if len(resp.Consumptions) == 0 || (resp.TotalCount != nil && read >= *resp.TotalCount) {
			return p.result(totals, updatedAt, itemErrors), nil
		}
	}
	return poller.Result{}, fmt.Errorf("scaleway consumption: still incomplete after %d pages", httpx.MaxPages)
}

func (p *consumptionPoller) result(totals map[groupKey]total, updatedAt string, itemErrors int) poller.Result {
	metrics := make([]prometheus.Metric, 0, len(totals)+1)
	for k, t := range totals {
		// Label values come from encoding/json, which yields valid UTF-8.
		metrics = append(metrics, prometheus.MustNewConstMetric(amountDesc, prometheus.GaugeValue,
			t.amount(), k.projectID, k.projectName, k.category, k.currency))
	}
	switch ts, err := time.Parse(time.RFC3339, updatedAt); {
	case updatedAt == "":
		p.logger.Debug("scaleway consumption has no updated_at; timestamp omitted")
	case err != nil:
		p.logger.Warn("scaleway consumption updated_at is not RFC 3339; timestamp omitted",
			"updated_at", capped(updatedAt), "error", capped(err.Error()))
		itemErrors++
	default:
		metrics = append(metrics, prometheus.MustNewConstMetric(updatedDesc, prometheus.GaugeValue,
			float64(ts.UnixNano())/1e9))
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors}
}

// maxLogValue caps remote strings written to the logs.
const maxLogValue = 64

// capped returns at most maxLogValue bytes of s, marking a cut.
func capped(s string) string {
	if len(s) <= maxLogValue {
		return s
	}
	return s[:maxLogValue] + "...(truncated)"
}
