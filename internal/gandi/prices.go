package gandi

import (
	"context"
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
	// query string short. The request asks for one process only, so a batch
	// returns at most pricesBatch products, well under its per_page.
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

	tlds := make([]string, 0, len(domains))
	for _, d := range domains {
		if t := domainTLD(d); t != "" && !slices.Contains(tlds, t) {
			tlds = append(tlds, t)
		}
	}
	slices.Sort(tlds)

	prices := map[string]float64{}
	var currency string
	for batch := range slices.Chunk(tlds, pricesBatch) {
		cur, got, err := p.fetch(ctx, batch)
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
	}

	var metrics []prometheus.Metric
	for _, d := range domains {
		v, ok := prices[domainTLD(d)]
		if !ok {
			p.logger.Warn("gandi domain has no one-year renewal price; skipped", "domain", capped(d.FQDN))
			itemErrors++
			continue
		}
		metrics = append(metrics, gauge(renewalPriceDesc, v, d.FQDN, currency))
	}
	if len(metrics) == 0 {
		return poller.Result{}, fmt.Errorf("gandi prices: no domain got a renewal price (%d skipped)", itemErrors)
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors}, nil
}

// fetch asks the renewal prices of one batch of TLDs and returns the response
// currency and the one-year price, excluding taxes, per TLD.
func (p *pricesPoller) fetch(ctx context.Context, tlds []string) (string, map[string]float64, error) {
	q := url.Values{}
	for _, t := range tlds {
		q.Add("name", "."+t)
	}
	q.Set("processes", "renew")
	q.Set("per_page", strconv.Itoa(pageSize))
	if p.sharingID != "" {
		q.Set("sharing_id", p.sharingID)
	}
	var list *priceList
	if _, err := p.client.GetJSON(ctx, pricesPath, q, &list); err != nil {
		return "", nil, fmt.Errorf("gandi prices: %w", err)
	}
	if list == nil {
		return "", nil, errors.New("gandi prices: response is not an object")
	}
	// The value is not echoed: it becomes a label only once validated.
	if !currencyPattern.MatchString(list.Currency) {
		return "", nil, errors.New("gandi prices: response currency is not a three-letter code")
	}
	out := map[string]float64{}
	for _, prod := range list.Products {
		if prod.Process != "renew" {
			continue
		}
		t := normalizeTLD(prod.Name)
		if _, done := out[t]; done || t == "" {
			continue
		}
		for _, pr := range prod.Prices {
			if pr.DurationUnit != "y" || pr.MinDuration > 1 || pr.MaxDuration < 1 || pr.PriceBeforeTaxes == nil {
				continue
			}
			if v := *pr.PriceBeforeTaxes; v >= 0 && !math.IsInf(v, 0) && !math.IsNaN(v) {
				out[t] = v
				break
			}
		}
	}
	return list.Currency, out, nil
}

// domainTLD returns the domain's TLD: the list's tld field when present, else
// everything after the first label of the fqdn (Gandi lists registered
// domains, so the rest of the name is the TLD, such as co.uk).
func domainTLD(d domain) string {
	if t := normalizeTLD(d.TLD); t != "" {
		return t
	}
	if _, rest, ok := strings.Cut(d.FQDN, "."); ok {
		return normalizeTLD(rest)
	}
	return ""
}

func normalizeTLD(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
}
