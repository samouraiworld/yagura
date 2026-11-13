// Package gandi polls the Gandi API for domain expiry and prepaid balances.
package gandi

import (
	"context"
	"encoding/json"
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
	domainsPath = "/v5/domain/domains"
	pageSize    = 100
)

type domainsPoller struct {
	client    *httpx.Client
	sharingID string
	interval  time.Duration
	logger    *slog.Logger
}

// NewDomainsPoller returns the poller of the domain list, named "gandi_domains".
// A non-empty sharingID restricts the listing to that organization.
func NewDomainsPoller(client *httpx.Client, sharingID string, interval time.Duration, logger *slog.Logger) poller.Poller {
	return &domainsPoller{client: client, sharingID: sharingID, interval: interval, logger: logger}
}

func (p *domainsPoller) Name() string            { return "gandi_domains" }
func (p *domainsPoller) Interval() time.Duration { return p.interval }

type domain struct {
	FQDN      string `json:"fqdn"`
	TLD       string `json:"tld"`
	Autorenew *bool  `json:"autorenew"`
	Dates     struct {
		RegistryEndsAt string `json:"registry_ends_at"`
	} `json:"dates"`
}

// Poll lists every domain page by page and builds one expiry and one
// autorenew series per distinct fqdn.
func (p *domainsPoller) Poll(ctx context.Context) (poller.Result, error) {
	domains, itemErrors, err := listDomains(ctx, p.client, p.sharingID, p.logger)
	if err != nil {
		return poller.Result{}, err
	}

	var metrics []prometheus.Metric
	for _, d := range domains {
		name := d.FQDN
		end, err := time.Parse(time.RFC3339, d.Dates.RegistryEndsAt)
		if err != nil {
			p.logger.Warn("gandi domain has no valid registry_ends_at; skipped",
				"domain", capped(name), "registry_ends_at", capped(d.Dates.RegistryEndsAt))
			itemErrors++
			continue
		}
		metrics = append(metrics, gauge(expiryDesc, float64(end.Unix()), name))
		if d.Autorenew == nil {
			p.logger.Warn("gandi domain has no autorenew; series omitted", "domain", capped(name))
			itemErrors++
			continue
		}
		v := 0.0
		if *d.Autorenew {
			v = 1
		}
		metrics = append(metrics, gauge(autorenewDesc, v, name))
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors}, nil
}

// listDomains lists every domain and decodes the entries. Entries that are not
// domain objects or have no fqdn are skipped with a warning and counted in
// itemErrors. A listing that shifts between page requests can repeat a domain,
// and two series with the same labels would make the registry fail to gather:
// it keeps one entry per fqdn, the last one seen, in first-seen order.
func listDomains(ctx context.Context, client *httpx.Client, sharingID string, logger *slog.Logger) (domains []domain, itemErrors int, err error) {
	raw, err := listRaw(ctx, client, sharingID)
	if err != nil {
		return nil, 0, err
	}
	var order []string
	byName := map[string]domain{}
	for _, item := range raw {
		var d domain
		if err := json.Unmarshal(item, &d); err != nil {
			logger.Warn("gandi domain entry is not a domain object; skipped", "error", capped(err.Error()))
			itemErrors++
			continue
		}
		if d.FQDN == "" {
			logger.Warn("gandi domain entry has no fqdn; skipped")
			itemErrors++
			continue
		}
		if _, seen := byName[d.FQDN]; !seen {
			order = append(order, d.FQDN)
		}
		byName[d.FQDN] = d
	}
	for _, name := range order {
		domains = append(domains, byName[name])
	}
	return domains, itemErrors, nil
}

// listRaw returns the raw domain entries of every page. It stops at a page with
// fewer than pageSize items, or once the raw count reaches the Total-Count
// header (when present, numeric and positive); httpx.MaxPages pages without a stop is
// an error.
func listRaw(ctx context.Context, client *httpx.Client, sharingID string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	for page := 1; page <= httpx.MaxPages; page++ {
		q := url.Values{}
		q.Set("per_page", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		if sharingID != "" {
			q.Set("sharing_id", sharingID)
		}
		var items []json.RawMessage
		hdr, err := client.GetJSON(ctx, domainsPath, q, &items)
		if err != nil {
			return nil, fmt.Errorf("gandi domains: page %d: %w", page, err)
		}
		if items == nil {
			return nil, fmt.Errorf("gandi domains: page %d: response is not an array", page)
		}
		all = append(all, items...)
		if len(items) < pageSize {
			return all, nil
		}
		if total, err := strconv.Atoi(hdr.Get("Total-Count")); err == nil && total > 0 && len(all) >= total {
			return all, nil
		}
	}
	return nil, fmt.Errorf("gandi domains: more than %d pages", httpx.MaxPages)
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
