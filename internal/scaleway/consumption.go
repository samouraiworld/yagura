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
	catalog        *Catalog
	history        *History
}

// NewConsumptionPoller returns the poller of the organization's month-to-date
// consumption, named "scaleway_consumption". Each successful poll replaces
// the projects of catalog and the current month of history; either may be
// nil.
func NewConsumptionPoller(client *httpx.Client, organizationID string, interval time.Duration, logger *slog.Logger, catalog *Catalog, history *History) poller.Poller {
	return &consumptionPoller{client: client, organizationID: organizationID, interval: interval, logger: logger, catalog: catalog, history: history}
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

// Poll reads the current month's consumption (billing_period omitted) and
// sums the rows per project, category and currency.
func (p *consumptionPoller) Poll(ctx context.Context) (poller.Result, error) {
	start := p.history.clock()
	read, err := readConsumption(ctx, p.client, p.organizationID, "", p.logger)
	if err != nil {
		return poller.Result{}, err
	}
	res := p.result(read.totals, read.updatedAt, read.itemErrors)
	p.history.observeCurrent(start, p.history.clock(), read.totals)
	return res, nil
}

// consumptionRead is one complete read of a billing period's consumption.
type consumptionRead struct {
	totals     map[groupKey]total
	updatedAt  string
	itemErrors int
}

// readConsumption reads every page of one billing period's consumption
// ("YYYY-MM"; empty means the current period) and sums the rows per project,
// name, category and currency. It stops once total_count rows are read or on
// an empty page; more than httpx.MaxPages pages fail the read. An empty list
// is a success with no totals.
func readConsumption(ctx context.Context, client *httpx.Client, organizationID, period string, logger *slog.Logger) (consumptionRead, error) {
	prefix := "scaleway consumption"
	if period != "" {
		prefix += " " + period
	}
	read := consumptionRead{totals: map[groupKey]total{}}
	rows := 0
	for page := 1; page <= httpx.MaxPages; page++ {
		query := url.Values{
			"organization_id": {organizationID},
			"page":            {strconv.Itoa(page)},
			"page_size":       {strconv.Itoa(pageSize)},
		}
		if period != "" {
			query.Set("billing_period", period)
		}
		var resp consumptionsPage
		if _, err := client.GetJSON(ctx, consumptionsPath, query, &resp); err != nil {
			return consumptionRead{}, fmt.Errorf("%s: page %d: %w", prefix, page, err)
		}
		if resp.Consumptions == nil {
			return consumptionRead{}, fmt.Errorf("%s: page %d: missing \"consumptions\" array", prefix, page)
		}
		if page == 1 {
			read.updatedAt = resp.UpdatedAt
		}
		for _, row := range resp.Consumptions {
			if row.Value == nil || row.Value.CurrencyCode == "" {
				logger.Warn("scaleway consumption row has no value or currency; row skipped",
					"billing_period", period, "project_id", capped(row.ProjectID), "category", capped(row.CategoryName))
				read.itemErrors++
				continue
			}
			k := groupKey{row.ProjectID, row.ProjectName, row.CategoryName, row.Value.CurrencyCode}
			t := read.totals[k]
			t.units += row.Value.Units
			t.nanos += int64(row.Value.Nanos)
			read.totals[k] = t
		}
		rows += len(resp.Consumptions)
		// A closed month is stored in place of the previous read: an empty
		// page before total_count is an incomplete answer, not the end.
		if period != "" && len(resp.Consumptions) == 0 && resp.TotalCount != nil && rows < *resp.TotalCount {
			return consumptionRead{}, fmt.Errorf("%s: page %d is empty after %d of %d rows", prefix, page, rows, *resp.TotalCount)
		}
		if len(resp.Consumptions) == 0 || (resp.TotalCount != nil && rows >= *resp.TotalCount) {
			return read, nil
		}
	}
	return consumptionRead{}, fmt.Errorf("%s: still incomplete after %d pages", prefix, httpx.MaxPages)
}

func (p *consumptionPoller) result(totals map[groupKey]total, updatedAt string, itemErrors int) poller.Result {
	metrics := make([]prometheus.Metric, 0, len(totals)+1)
	keys := make([]groupKey, 0, len(totals))
	for k, t := range totals {
		keys = append(keys, k)
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
	p.catalog.set(keys)
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
