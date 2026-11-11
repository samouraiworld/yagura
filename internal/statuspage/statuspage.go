// Package statuspage polls Atlassian Statuspage-compatible status pages.
package statuspage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samourai-crew/yagura/internal/httpx"
	"github.com/samourai-crew/yagura/internal/poller"
)

const (
	statusPath      = "/api/v2/status.json"
	incidentsPath   = "/api/v2/incidents/unresolved.json"
	maintenancePath = "/api/v2/scheduled-maintenances/active.json"
)

type statusPoller struct {
	name     string
	client   *httpx.Client
	interval time.Duration
	logger   *slog.Logger
}

// NewPoller returns the poller of one status page, named "statuspage_<name>".
// The provider label of its metrics is name.
func NewPoller(name string, client *httpx.Client, interval time.Duration, logger *slog.Logger) poller.Poller {
	return &statusPoller{name: name, client: client, interval: interval, logger: logger}
}

func (p *statusPoller) Name() string            { return "statuspage_" + p.name }
func (p *statusPoller) Interval() time.Duration { return p.interval }

type statusResponse struct {
	Page struct {
		UpdatedAt string `json:"updated_at"`
	} `json:"page"`
	Status struct {
		Indicator string `json:"indicator"`
	} `json:"status"`
}

// Poll reads status.json (required), then the incidents and maintenances
// endpoints, which some pages do not serve.
func (p *statusPoller) Poll(ctx context.Context) (poller.Result, error) {
	var st statusResponse
	if _, err := p.client.GetJSON(ctx, statusPath, nil, &st); err != nil {
		return poller.Result{}, fmt.Errorf("statuspage %s: status: %w", p.name, err)
	}
	metrics := []prometheus.Metric{gauge(indicatorDesc, p.indicatorValue(st.Status.Indicator), p.name)}
	if st.Page.UpdatedAt == "" {
		p.logger.Warn("statuspage page has no updated_at; timestamp omitted", "provider", p.name)
	} else if ts, err := time.Parse(time.RFC3339, st.Page.UpdatedAt); err != nil {
		p.logger.Warn("statuspage updated_at is not RFC 3339; timestamp omitted", "provider", p.name)
	} else {
		metrics = append(metrics, gauge(updatedDesc, float64(ts.UnixNano())/1e9, p.name))
	}

	var inc struct {
		Incidents []struct{} `json:"incidents"`
	}
	n, ok, err := p.optionalCount(ctx, incidentsPath, &inc, func() ([]struct{}, string) { return inc.Incidents, "incidents" })
	if err != nil {
		return poller.Result{}, err
	}
	if ok {
		metrics = append(metrics, gauge(incidentsDesc, float64(n), p.name))
	}

	var maint struct {
		Maintenances []struct{} `json:"scheduled_maintenances"`
	}
	n, ok, err = p.optionalCount(ctx, maintenancePath, &maint, func() ([]struct{}, string) { return maint.Maintenances, "scheduled_maintenances" })
	if err != nil {
		return poller.Result{}, err
	}
	if ok {
		metrics = append(metrics, gauge(maintenancesDesc, float64(n), p.name))
	}
	return poller.Result{Metrics: metrics}, nil
}

// optionalCount fetches an optional endpoint. A 404 or a non-JSON body gives
// ok=false without error; any other failure, or valid JSON missing its
// top-level array, is an error.
func (p *statusPoller) optionalCount(ctx context.Context, path string, out any, array func() ([]struct{}, string)) (n int, ok bool, err error) {
	if _, err := p.client.GetJSON(ctx, path, nil, out); err != nil {
		if httpx.IsNotFound(err) || errors.Is(err, httpx.ErrDecode) {
			p.logger.Debug("statuspage optional endpoint unavailable; metric omitted", "provider", p.name, "path", path)
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("statuspage %s: %s: %w", p.name, path, err)
	}
	items, field := array()
	if items == nil {
		return 0, false, fmt.Errorf("statuspage %s: %s: missing %q array", p.name, path, field)
	}
	return len(items), true, nil
}

func (p *statusPoller) indicatorValue(s string) float64 {
	switch s {
	case "none":
		return 0
	case "minor":
		return 1
	case "major":
		return 2
	case "critical":
		return 3
	}
	p.logger.Warn("statuspage unknown indicator", "provider", p.name, "indicator", s)
	return -1
}
