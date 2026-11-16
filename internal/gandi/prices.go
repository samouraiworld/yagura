package gandi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
)

const (
	pricesPath = "/v5/billing/price/domain"
	// pricesBatch caps the TLDs asked in one price request, which keeps the
	// query string short. Asking one process only, a batch should fit in one
	// page; pages are still followed in case Gandi returns more products.
	pricesBatch = 50
)

// currencyPattern matches an ISO 4217 code, the only currency label allowed.
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

type pricesPoller struct {
	client    *httpx.Client
	sharingID string
	interval  time.Duration
	logger    *slog.Logger
}

// NewPricesPoller returns the poller of domain renewal prices, named
// "gandi_prices". A non-empty sharingID restricts the domain listing to that
// organization and asks for that organization's prices.
func NewPricesPoller(client *httpx.Client, sharingID string, interval time.Duration, logger *slog.Logger) poller.Poller {
	return &pricesPoller{client: client, sharingID: sharingID, interval: interval, logger: logger}
}

func (p *pricesPoller) Name() string            { return "gandi_prices" }
func (p *pricesPoller) Interval() time.Duration { return p.interval }

type priceList struct {
	Currency string `json:"currency"`
	Products []struct {
		Name    string `json:"name"`
		Process string `json:"process"`
		Prices  []struct {
			DurationUnit     string   `json:"duration_unit"`
			MinDuration      int      `json:"min_duration"`
			MaxDuration      int      `json:"max_duration"`
			PriceBeforeTaxes *float64 `json:"price_before_taxes"`
		} `json:"prices"`
	} `json:"products"`
}

// Poll lists the domains, asks the one-year renewal price of every distinct
// TLD and exports that price, excluding taxes, for each domain. A domain whose
// TLD has no usable renewal price is skipped and counted as an item error; a
// poll where no domain gets a price fails.
func (p *pricesPoller) Poll(ctx context.Context) (poller.Result, error) {
	domains, itemErrors, err := listDomains(ctx, p.client, p.sharingID, p.logger)
	if err != nil {
		return poller.Result{}, err
	}
	if len(domains) == 0 {
		return poller.Result{ItemErrors: itemErrors}, nil
	}

	seen := map[string]bool{}
	var tlds []string
	for _, d := range domains {
		if t := domainTLD(d); t != "" && !seen[t] {
			seen[t] = true
			tlds = append(tlds, t)
		}
	}
	slices.Sort(tlds)

	prices := map[string]float64{}
	noYear := map[string]bool{}
	var currency string
	for batch := range slices.Chunk(tlds, pricesBatch) {
		cur, got, multi, err := p.fetch(ctx, batch)
		if err != nil {
			return poller.Result{}, err
		}
		if currency != "" && cur != currency {
			return poller.Result{}, errors.New("gandi prices: responses use different currencies")
		}
		currency = cur
		for t, v := range got {
			prices[t] = v
		}
		for t := range multi {
			noYear[t] = true
		}
	}

	var metrics []prometheus.Metric
	var unpriced int
	for _, d := range domains {
		t := domainTLD(d)
		v, ok := prices[t]
		if !ok && noYear[t] {
			// Gandi does not renew this TLD for one year: retries cannot change
			// that, so it is no item error, but the estimate leaves it out.
			p.logger.Warn("gandi domain's TLD has no one-year renewal; left out of the prices", "domain", capped(d.FQDN))
			continue
		}
		if !ok {
			p.logger.Warn("gandi domain has no one-year renewal price; skipped", "domain", capped(d.FQDN))
			itemErrors++
			unpriced++
			continue
		}
		metrics = append(metrics, gauge(renewalPriceDesc, v, d.FQDN, currency))
	}
	if len(metrics) == 0 && unpriced > 0 {
		return poller.Result{}, fmt.Errorf("gandi prices: no domain got a renewal price (%d skipped)", unpriced)
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors}, nil
}

// fetch asks the renewal prices of one batch of TLDs and returns the response
// currency, the one-year price, excluding taxes, per TLD, and the TLDs whose
// renewal has valid prices but none for one year. It follows pages
// like the domain listing: it stops at a page with fewer than pageSize
// products or once the Total-Count header is reached.
func (p *pricesPoller) fetch(ctx context.Context, tlds []string) (string, map[string]float64, map[string]bool, error) {
	var currency string
	out := map[string]float64{}
	noYear := map[string]bool{}
	var read int
	for page := 1; page <= httpx.MaxPages; page++ {
		q := url.Values{}
		for _, t := range tlds {
			q.Add("name", "."+t)
		}
		q.Set("processes", "renew")
		q.Set("per_page", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		if p.sharingID != "" {
			q.Set("sharing_id", p.sharingID)
		}
		var list *priceList
		hdr, err := p.client.GetJSON(ctx, pricesPath, q, &list)
		if err != nil {
			return "", nil, nil, fmt.Errorf("gandi prices: page %d: %w", page, err)
		}
		if list == nil {
			return "", nil, nil, fmt.Errorf("gandi prices: page %d: response is not an object", page)
		}
		// The value is not echoed: it becomes a label only once validated.
		if !currencyPattern.MatchString(list.Currency) {
			return "", nil, nil, errors.New("gandi prices: response currency is not a three-letter code")
		}
		if currency != "" && list.Currency != currency {
			return "", nil, nil, errors.New("gandi prices: responses use different currencies")
		}
		currency = list.Currency
		collectPrices(list, out, noYear)
		read += len(list.Products)
		if len(list.Products) < pageSize {
			return currency, out, noYear, nil
		}
		if total, err := strconv.Atoi(hdr.Get("Total-Count")); err == nil && total > 0 && read >= total {
			return currency, out, noYear, nil
		}
	}
	return "", nil, nil, fmt.Errorf("gandi prices: more than %d pages", httpx.MaxPages)
}

// collectPrices adds to out the one-year price, excluding taxes, of every
// renew product of list whose TLD has no price yet, and to noYear the TLDs
// whose renew product has valid prices, none of them for one year.
func collectPrices(list *priceList, out map[string]float64, noYear map[string]bool) {
	for _, prod := range list.Products {
		if prod.Process != "renew" {
			continue
		}
		t := normalizeTLD(prod.Name)
		if _, done := out[t]; done || t == "" {
			continue
		}
		var valid bool
		for _, pr := range prod.Prices {
			if pr.PriceBeforeTaxes == nil {
				continue
			}
			v := *pr.PriceBeforeTaxes
			if v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
				continue
			}
			valid = true
			if pr.DurationUnit == "y" && pr.MinDuration <= 1 && pr.MaxDuration >= 1 {
				out[t] = v
				break
			}
		}
		if _, done := out[t]; !done && valid {
			noYear[t] = true
		}
	}
}

// domainTLD returns the domain's TLD: the list's tld field when present, else
// everything after the first label of the fqdn (Gandi lists registered
// domains, so the rest of the name is the TLD, such as co.uk).
func domainTLD(d domain) string {
	var tld string
	if json.Unmarshal(d.TLD, &tld) == nil {
		if t := normalizeTLD(tld); t != "" {
			return t
		}
	}
	if _, rest, ok := strings.Cut(d.FQDN, "."); ok {
		return normalizeTLD(rest)
	}
	return ""
}

func normalizeTLD(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
}
