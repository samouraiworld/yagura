// Package netlify polls the Netlify API for the last production deploy of each site and for build-minute usage.
package netlify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
)

const (
	sitesPath = "/api/v1/sites"
	pageSize  = 100
)

type site struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccountSlug string `json:"account_slug"`
}

type deploy struct {
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
}

// validID reports whether id is safe to put in a URL path: 1 to 64 ASCII
// letters, digits or hyphens.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// hasNextLink reports whether the Link header values announce a next page
// (a link whose rel parameter contains "next"). The URL itself is never used.
func hasNextLink(h http.Header) bool {
	for _, v := range h.Values("Link") {
		for _, link := range strings.Split(v, ",") {
			_, params, ok := strings.Cut(link, ">")
			if !ok {
				continue
			}
			for _, param := range strings.Split(params, ";") {
				k, val, ok := strings.Cut(strings.TrimSpace(param), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(k), "rel") {
					continue
				}
				for _, rel := range strings.Fields(strings.Trim(strings.TrimSpace(val), `"`)) {
					if strings.EqualFold(rel, "next") {
						return true
					}
				}
			}
		}
	}
	return false
}

// listSites returns the sites of every page, requesting page=N itself and
// using the Link header only as a signal to continue.
func listSites(ctx context.Context, c *httpx.Client, logger *slog.Logger) ([]site, int, error) {
	var raw []json.RawMessage
	for page := 1; ; page++ {
		if page > httpx.MaxPages {
			return nil, 0, fmt.Errorf("netlify sites: more than %d pages", httpx.MaxPages)
		}
		q := url.Values{}
		q.Set("filter", "all")
		q.Set("per_page", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		var items []json.RawMessage
		hdr, err := c.GetJSON(ctx, sitesPath, q, &items)
		if err != nil {
			return nil, 0, fmt.Errorf("netlify sites: page %d: %w", page, err)
		}
		if items == nil {
			return nil, 0, fmt.Errorf("netlify sites: page %d: response is not an array", page)
		}
		raw = append(raw, items...)
		if len(items) == 0 || !hasNextLink(hdr) {
			break
		}
	}

	var skipped int
	var order []string
	byID := map[string]site{}
	for _, item := range raw {
		var s site
		if err := json.Unmarshal(item, &s); err != nil {
			logger.Warn("netlify site entry is not a site object; skipped", "error", capped(err.Error()))
			skipped++
			continue
		}
		if !validID(s.ID) {
			logger.Warn("netlify site entry has a missing or invalid id; skipped", "id", capped(s.ID))
			skipped++
			continue
		}
		if s.Name == "" {
			logger.Warn("netlify site has no name; skipped", "site_id", s.ID)
			skipped++
			continue
		}
		if _, seen := byID[s.ID]; !seen {
			order = append(order, s.ID)
		}
		byID[s.ID] = s
	}
	out := make([]site, 0, len(order))
	names := map[string]bool{}
	for _, id := range order {
		s := byID[id]
		if names[s.Name] {
			logger.Warn("netlify site name repeats an earlier site; skipped", "site_id", s.ID, "site", capped(s.Name))
			skipped++
			continue
		}
		names[s.Name] = true
		out = append(out, s)
	}
	return out, skipped, nil
}

// logItemError logs the failure of one per-site call: a gone site at debug
// level, other failures as warnings. Auth errors, skipped items (which log
// their own warning) and errors after cancellation are not logged.
func logItemError(ctx context.Context, logger *slog.Logger, what string, id string, err error) {
	switch {
	case httpx.IsNotFound(err):
		logger.Debug("netlify site gone; "+what+" omitted", "site_id", id)
	case httpx.IsAuthError(err), errors.Is(err, poller.ErrItemSkipped), ctx.Err() != nil:
	default:
		logger.Warn("netlify "+what+" call failed", "site_id", id, "error", capped(err.Error()))
	}
}

type deploysPoller struct {
	client   *httpx.Client
	interval time.Duration
	logger   *slog.Logger
}

// NewDeploysPoller returns the poller of the last production deploy of each
// site, named "netlify_deploys".
func NewDeploysPoller(client *httpx.Client, interval time.Duration, logger *slog.Logger) poller.Poller {
	return &deploysPoller{client: client, interval: interval, logger: logger}
}

func (p *deploysPoller) Name() string            { return "netlify_deploys" }
func (p *deploysPoller) Interval() time.Duration { return p.interval }

// Poll lists the sites, then fetches the last production deploy of each.
func (p *deploysPoller) Poll(ctx context.Context) (poller.Result, error) {
	sites, itemErrors, err := listSites(ctx, p.client, p.logger)
	if err != nil {
		return poller.Result{}, err
	}
	metrics, n, err := poller.RunItems(ctx, sites, p.deploy)
	itemErrors += n
	if err != nil {
		return poller.Result{}, err
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors}, nil
}

// deploy fetches the last production deploy of one site. No deploy emits
// nothing; a deploy without state is skipped; an unusable created_at omits the
// timestamp series and counts as a skipped item.
func (p *deploysPoller) deploy(ctx context.Context, s site) ([]prometheus.Metric, error) {
	q := url.Values{}
	q.Set("production", "true")
	q.Set("per_page", "1")
	var deploys []deploy
	_, err := p.client.GetJSON(ctx, sitesPath+"/"+s.ID+"/deploys", q, &deploys)
	if err == nil && deploys == nil {
		err = errors.New("response is not an array")
	}
	if err != nil {
		err = fmt.Errorf("netlify deploys of %s: %w", s.ID, err)
		logItemError(ctx, p.logger, "deploys", s.ID, err)
		return nil, err
	}
	if len(deploys) == 0 {
		return nil, nil
	}
	d := deploys[0]
	if d.State == "" {
		p.logger.Warn("netlify deploy has no state; skipped", "site_id", s.ID)
		return nil, fmt.Errorf("netlify deploy of %s has no state: %w", s.ID, poller.ErrItemSkipped)
	}
	failed := 0.0
	if d.State == "error" {
		failed = 1
	}
	out := []prometheus.Metric{
		gauge(deployInfoDesc, 1, s.Name, s.AccountSlug, d.State),
		gauge(deployFailedDesc, failed, s.Name),
	}
	created, err := time.Parse(time.RFC3339, d.CreatedAt)
	if err != nil {
		p.logger.Warn("netlify deploy has a missing or invalid created_at; timestamp omitted", "site_id", s.ID)
		return out, fmt.Errorf("netlify deploy of %s has an invalid created_at: %w", s.ID, poller.ErrItemSkipped)
	}
	return append(out, gauge(deployCreatedDesc, float64(created.Unix()), s.Name)), nil
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
