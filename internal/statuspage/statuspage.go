// Package statuspage polls Atlassian Statuspage-compatible status pages.
package statuspage

import (
	"context"
	"encoding/json"
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
	var itemErrors int
	if st.Status.Indicator == "" {
		itemErrors++ // a required field is missing: alertable through the item-error gauge
	}
	metrics := []prometheus.Metric{gauge(indicatorDesc, p.indicatorValue(st.Status.Indicator), p.name)}
	if st.Page.UpdatedAt == "" {
		p.logger.Warn("statuspage page has no updated_at; timestamp omitted", "provider", p.name)
		itemErrors++
	} else if ts, err := time.Parse(time.RFC3339, st.Page.UpdatedAt); err != nil {
		p.logger.Warn("statuspage updated_at is not RFC 3339; timestamp omitted",
			"provider", p.name, "updated_at", capped(st.Page.UpdatedAt), "error", capped(err.Error()))
		itemErrors++
	} else {
		metrics = append(metrics, gauge(updatedDesc, float64(ts.UnixNano())/1e9, p.name))
	}

	n, ok, err := p.optionalCount(ctx, incidentsPath, "incidents")
	if err != nil {
		return poller.Result{}, err
	}
	if ok {
		metrics = append(metrics, gauge(incidentsDesc, float64(n), p.name))
	}

	n, ok, err = p.optionalCount(ctx, maintenancePath, "scheduled_maintenances")
	if err != nil {
		return poller.Result{}, err
	}
	if ok {
		metrics = append(metrics, gauge(maintenancesDesc, float64(n), p.name))
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors}, nil
}

// optionalCount fetches an optional endpoint and counts the items of its
// top-level array named key. A 404, a non-JSON body or a top-level value that
// is not a JSON object (not Statuspage-shaped) gives ok=false without error.
// Any other failure, or an object whose key is absent, null or not an array,
// is an error.
func (p *statusPoller) optionalCount(ctx context.Context, path, key string) (n int, ok bool, err error) {
	var raw json.RawMessage
	if _, err := p.client.GetJSON(ctx, path, nil, &raw); err != nil {
		if httpx.IsNotFound(err) || errors.Is(err, httpx.ErrDecode) {
			p.logger.Debug("statuspage optional endpoint unavailable; metric omitted", "provider", p.name, "path", path)
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("statuspage %s: %s: %w", p.name, path, err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		p.logger.Debug("statuspage optional endpoint is not a JSON object; metric omitted", "provider", p.name, "path", path)
		return 0, false, nil
	}
	var items []struct{}
	if err := json.Unmarshal(obj[key], &items); err != nil || items == nil {
		return 0, false, fmt.Errorf("statuspage %s: %s: missing %q array", p.name, path, key)
	}
	return len(items), true, nil
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
	p.logger.Warn("statuspage unknown indicator", "provider", p.name, "indicator", capped(s))
	return -1
}
