package netlify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
)

const accountsPath = "/api/v1/accounts"

type account struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

type buildStatus struct {
	Minutes *struct {
		Current         *float64        `json:"current"`
		IncludedMinutes json.RawMessage `json:"included_minutes"`
		PeriodEndDate   string          `json:"period_end_date"`
	} `json:"minutes"`
}

// listAccounts returns the accounts, dropping duplicates and unusable entries
// (counted in the second result).
func listAccounts(ctx context.Context, c *httpx.Client, logger *slog.Logger) ([]account, int, error) {
	var raw []json.RawMessage
	if _, err := c.GetJSON(ctx, accountsPath, nil, &raw); err != nil {
		return nil, 0, fmt.Errorf("netlify accounts: %w", err)
	}
	if raw == nil {
		return nil, 0, errors.New("netlify accounts: response is not an array")
	}
	var skipped int
	var order []string
	byID := map[string]account{}
	for _, item := range raw {
		var a account
		if err := json.Unmarshal(item, &a); err != nil {
			logger.Warn("netlify account entry is not an account object; skipped", "error", capped(err.Error()))
			skipped++
			continue
		}
		if !validID(a.ID) {
			logger.Warn("netlify account entry has a missing or invalid id; skipped", "id", capped(a.ID))
			skipped++
			continue
		}
		if a.Slug == "" {
			logger.Warn("netlify account has no slug; skipped", "account_id", a.ID)
			skipped++
			continue
		}
		if _, seen := byID[a.ID]; !seen {
			order = append(order, a.ID)
		}
		byID[a.ID] = a
	}
	out := make([]account, 0, len(order))
	slugs := map[string]bool{}
	for _, id := range order {
		a := byID[id]
		if slugs[a.Slug] {
			logger.Warn("netlify account slug repeats an earlier account; skipped", "account_id", a.ID, "account", capped(a.Slug))
			skipped++
			continue
		}
		slugs[a.Slug] = true
		out = append(out, a)
	}
	return out, skipped, nil
}

type buildsPoller struct {
	client   *httpx.Client
	interval time.Duration
	logger   *slog.Logger
}

// NewBuildsPoller returns the poller of the build-minute usage of each
// account, named "netlify_builds".
func NewBuildsPoller(client *httpx.Client, interval time.Duration, logger *slog.Logger) poller.Poller {
	return &buildsPoller{client: client, interval: interval, logger: logger}
}

func (p *buildsPoller) Name() string            { return "netlify_builds" }
func (p *buildsPoller) Interval() time.Duration { return p.interval }

// Poll lists the accounts, then fetches the build status of each.
func (p *buildsPoller) Poll(ctx context.Context) (poller.Result, error) {
	accounts, itemErrors, err := listAccounts(ctx, p.client, p.logger)
	if err != nil {
		return poller.Result{}, err
	}
	metrics, n, err := poller.RunItems(ctx, accounts, p.status)
	itemErrors += n
	if err != nil {
		return poller.Result{}, err
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors}, nil
}

// status fetches the build minutes of one account. No status emits nothing.
// A missing minutes object skips the item; a missing current or unusable
// period end omits that series and counts as a skipped item; an unusable
// included_minutes only omits its series.
func (p *buildsPoller) status(ctx context.Context, a account) ([]prometheus.Metric, error) {
	var statuses []buildStatus
	_, err := p.client.GetJSON(ctx, "/api/v1/"+a.ID+"/builds/status", nil, &statuses)
	if err == nil && statuses == nil {
		err = errors.New("response is not an array")
	}
	if err != nil {
		err = fmt.Errorf("netlify build status of %s: %w", a.ID, err)
		logItemError(ctx, p.logger, "build status", "account_id", a.ID, err)
		return nil, err
	}
	if len(statuses) == 0 {
		return nil, nil
	}
	m := statuses[0].Minutes
	if m == nil {
		p.logger.Warn("netlify build status has no minutes; skipped", "account_id", a.ID)
		return nil, fmt.Errorf("netlify build status of %s has no minutes: %w", a.ID, poller.ErrItemSkipped)
	}
	var out []prometheus.Metric
	incomplete := false
	if m.Current != nil {
		out = append(out, gauge(buildUsedDesc, *m.Current, a.Slug))
	} else {
		p.logger.Warn("netlify build status has no current minutes; series omitted", "account_id", a.ID)
		incomplete = true
	}
	if inc, ok := parseIncluded(m.IncludedMinutes); ok {
		out = append(out, gauge(buildIncludedDesc, inc, a.Slug))
	}
	if end, err := time.Parse(time.RFC3339, m.PeriodEndDate); err == nil {
		out = append(out, gauge(buildPeriodEndDesc, float64(end.Unix()), a.Slug))
	} else {
		p.logger.Warn("netlify build status has a missing or invalid period_end_date; series omitted", "account_id", a.ID)
		incomplete = true
	}
	if incomplete {
		return out, fmt.Errorf("netlify build status of %s is incomplete: %w", a.ID, poller.ErrItemSkipped)
	}
	return out, nil
}

// parseIncluded reads included_minutes, sent as a JSON string or number. Null,
// absent, empty, unparsable, NaN, infinite and negative values report false.
func parseIncluded(raw json.RawMessage) (float64, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		text = string(raw) // not a string: a number, or something ParseFloat rejects
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v, true
}
