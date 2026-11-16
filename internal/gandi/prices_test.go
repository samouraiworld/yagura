package gandi

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/testkit"
)

const (
	pricesListKey = "GET " + listPath + "?page=1&per_page=100"
	pricesKey     = "GET /v5/billing/price/domain?name=.co.uk&name=.com&name=.net&page=1&per_page=100&processes=renew"
)

func newPrices(t *testing.T, sharingID string, routes map[string]testkit.Route, logs *bytes.Buffer) (poller.Poller, *httptest.Server) {
	t.Helper()
	srv := testkit.NewServer(t, routes)
	client := httpx.New(httpx.Options{Provider: "gandi", BaseURL: srv.URL})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewPricesPoller(client, sharingID, time.Minute, logger), srv
}

type priceSeries struct {
	value    float64
	currency string
}

// priceValues gathers the metrics through a pedantic registry and returns
// domain -> renewal price series.
func priceValues(t *testing.T, res poller.Result) map[string]priceSeries {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(testkit.Collector(res.Metrics))
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]priceSeries{}
	for _, f := range fams {
		if f.GetName() != "yagura_gandi_domain_renewal_price" {
			t.Fatalf("unexpected family %s", f.GetName())
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if len(labels) != 2 {
				t.Fatalf("labels = %v, want domain and currency", labels)
			}
			out[labels["domain"]] = priceSeries{m.GetGauge().GetValue(), labels["currency"]}
		}
	}
	return out
}

func TestPricesPerDomain(t *testing.T) {
	p, srv := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {File: "testdata/prices_domains.json"},
		pricesKey:     {File: "testdata/prices_renew.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]priceSeries{
		"example.com":      {11.5, "EUR"},
		"shop.example.com": {11.5, "EUR"},
		"example.co.uk":    {7.25, "EUR"},
		"example.net":      {14, "EUR"},
	}
	got := priceValues(t, res)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("prices = %v, want %v", got, want)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
	}
	if n := testkit.Hits(srv, pricesKey); n != 1 {
		t.Errorf("price requests = %d, want 1 for every TLD", n)
	}
	if p.Name() != "gandi_prices" || p.Interval() != time.Minute {
		t.Errorf("name %q interval %v", p.Name(), p.Interval())
	}
}

func TestPricesSharingID(t *testing.T) {
	p, _ := newPrices(t, billingID, map[string]testkit.Route{
		pricesListKey + "&sharing_id=" + billingID: {File: "testdata/prices_domains.json"},
		pricesKey + "&sharing_id=" + billingID:     {File: "testdata/prices_renew.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(priceValues(t, res)); n != 4 {
		t.Errorf("series = %d, want 4", n)
	}
}

func TestPricesTLDWithoutRenewPrice(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {File: "testdata/prices_domains.json"},
		pricesKey: {Body: `{"currency":"EUR","products":[
			{"name":".com","process":"renew","prices":[{"duration_unit":"y","min_duration":1,"max_duration":10,"price_before_taxes":11.5}]},
			{"name":".net","process":"renew","prices":[{"duration_unit":"y","min_duration":1,"max_duration":10,"price_before_taxes":-1}]},
			{"name":".co.uk","process":"renew","prices":[{"duration_unit":"y","min_duration":1,"max_duration":10}]}
		]}`},
	}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	got := priceValues(t, res)
	if len(got) != 2 || got["example.com"].value != 11.5 || got["shop.example.com"].value != 11.5 {
		t.Errorf("prices = %v, want the two .com domains only", got)
	}
	if res.ItemErrors != 2 {
		t.Errorf("ItemErrors = %d, want 2 (.net negative, .co.uk without price)", res.ItemErrors)
	}
	if !strings.Contains(logs.String(), "example.co.uk") {
		t.Errorf("skipped domain not logged: %s", logs.String())
	}
}

func TestPricesNoDomainNoPriceCall(t *testing.T) {
	p, _ := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {File: "testdata/domains_empty.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Metrics) != 0 || res.ItemErrors != 0 {
		t.Errorf("got %d metrics, %d item errors; want none", len(res.Metrics), res.ItemErrors)
	}
}

func TestPricesNoDomainPricedIsFailure(t *testing.T) {
	p, _ := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {File: "testdata/prices_domains.json"},
		pricesKey:     {Body: `{"currency":"EUR","products":[]}`},
	}, &bytes.Buffer{})
	if _, err := poll(t, p); err == nil || !strings.Contains(err.Error(), "gandi prices") {
		t.Fatalf("want a gandi prices poll error, got %v", err)
	}
}

func TestPricesBadCurrency(t *testing.T) {
	for _, cur := range []string{"", "euro", "eu", "EU1", "€€€"} {
		t.Run(cur, func(t *testing.T) {
			p, _ := newPrices(t, "", map[string]testkit.Route{
				pricesListKey: {File: "testdata/prices_domains.json"},
				pricesKey: {Body: `{"currency":"` + cur + `","products":[
					{"name":".com","process":"renew","prices":[{"duration_unit":"y","min_duration":1,"max_duration":10,"price_before_taxes":11.5}]}
				]}`},
			}, &bytes.Buffer{})
			_, err := poll(t, p)
			if err == nil || !strings.Contains(err.Error(), "currency") {
				t.Fatalf("want a currency error, got %v", err)
			}
			if cur != "" && strings.Contains(err.Error(), cur) {
				t.Errorf("error echoes the value: %v", err)
			}
		})
	}
}

func TestPricesCallFails(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p, _ := newPrices(t, "", map[string]testkit.Route{
				pricesListKey: {File: "testdata/prices_domains.json"},
				pricesKey:     {Status: status, Body: `{"message":"no"}`},
			}, &bytes.Buffer{})
			_, err := poll(t, p)
			var se *httpx.StatusError
			if !errors.As(err, &se) || se.Code != status {
				t.Fatalf("want status error %d, got %v", status, err)
			}
		})
	}
}

func TestPricesListFails(t *testing.T) {
	p, _ := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {Status: http.StatusUnauthorized, Body: `{}`},
	}, &bytes.Buffer{})
	if _, err := poll(t, p); err == nil {
		t.Fatal("want an error when the domain list fails")
	}
}

func TestPricesNotAnObject(t *testing.T) {
	p, _ := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {File: "testdata/prices_domains.json"},
		pricesKey:     {Body: `[]`},
	}, &bytes.Buffer{})
	if _, err := poll(t, p); err == nil || !strings.Contains(err.Error(), "gandi prices") {
		t.Fatalf("want a gandi prices error, got %v", err)
	}
}

// TestPricesBatches checks that many TLDs are split over several requests so
// the query string stays short.
func TestPricesBatches(t *testing.T) {
	var list, first, second strings.Builder
	list.WriteString("[")
	first.WriteString(`{"currency":"EUR","products":[`)
	second.WriteString(`{"currency":"EUR","products":[`)
	firstKey := "GET /v5/billing/price/domain?"
	secondKey := "GET /v5/billing/price/domain?"
	for i := range 60 {
		tld := fmt.Sprintf("t%02d", i)
		if i > 0 {
			list.WriteString(",")
		}
		fmt.Fprintf(&list, `{"fqdn":"example.%s","tld":"%s","autorenew":true,"dates":{"registry_ends_at":"2027-01-01T00:00:00Z"}}`, tld, tld)
		product := fmt.Sprintf(`{"name":".%s","process":"renew","prices":[{"duration_unit":"y","min_duration":1,"max_duration":1,"price_before_taxes":%d}]}`, tld, i+1)
		if i < pricesBatch {
			if i > 0 {
				first.WriteString(",")
			}
			first.WriteString(product)
			firstKey += "name=." + tld + "&"
		} else {
			if i > pricesBatch {
				second.WriteString(",")
			}
			second.WriteString(product)
			secondKey += "name=." + tld + "&"
		}
	}
	list.WriteString("]")
	first.WriteString("]}")
	second.WriteString("]}")
	p, _ := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {Body: list.String()},
		firstKey + "page=1&per_page=100&processes=renew":  {Body: first.String()},
		secondKey + "page=1&per_page=100&processes=renew": {Body: second.String()},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	got := priceValues(t, res)
	if len(got) != 60 || got["example.t00"].value != 1 || got["example.t59"].value != 60 {
		t.Errorf("got %d series, t00=%v t59=%v", len(got), got["example.t00"], got["example.t59"])
	}
}

// TestPricesPaginated checks that a full page of products is followed by the
// next page, in case Gandi returns more products than the TLDs asked.
func TestPricesPaginated(t *testing.T) {
	var full strings.Builder
	full.WriteString(`{"currency":"EUR","products":[`)
	for i := range pageSize {
		if i > 0 {
			full.WriteString(",")
		}
		full.WriteString(`{"name":".com","process":"create","prices":[{"duration_unit":"y","min_duration":1,"max_duration":10,"price_before_taxes":99}]}`)
	}
	full.WriteString("]}")
	base := "GET /v5/billing/price/domain?name=.co.uk&name=.com&name=.net&"
	p, srv := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {File: "testdata/prices_domains.json"},
		base + "page=1&per_page=100&processes=renew": {Body: full.String()},
		base + "page=2&per_page=100&processes=renew": {File: "testdata/prices_renew.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := priceValues(t, res); len(got) != 4 || got["example.com"].value != 11.5 {
		t.Errorf("prices = %v, want the four domains from page 2", got)
	}
	if n := testkit.Hits(srv, base+"page=2&per_page=100&processes=renew"); n != 1 {
		t.Errorf("page 2 requests = %d, want 1", n)
	}
}

func TestPricesCurrencyMismatchAcrossBatches(t *testing.T) {
	var list strings.Builder
	list.WriteString("[")
	keys := [2]string{"GET /v5/billing/price/domain?", "GET /v5/billing/price/domain?"}
	for i := range pricesBatch + 1 {
		tld := fmt.Sprintf("t%02d", i)
		if i > 0 {
			list.WriteString(",")
		}
		fmt.Fprintf(&list, `{"fqdn":"example.%s","tld":"%s","autorenew":true,"dates":{"registry_ends_at":"2027-01-01T00:00:00Z"}}`, tld, tld)
		keys[i/pricesBatch] += "name=." + tld + "&"
	}
	list.WriteString("]")
	product := `{"name":".t00","process":"renew","prices":[{"duration_unit":"y","min_duration":1,"max_duration":1,"price_before_taxes":1}]}`
	p, _ := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {Body: list.String()},
		keys[0] + "page=1&per_page=100&processes=renew": {Body: `{"currency":"EUR","products":[` + product + `]}`},
		keys[1] + "page=1&per_page=100&processes=renew": {Body: `{"currency":"USD","products":[]}`},
	}, &bytes.Buffer{})
	if _, err := poll(t, p); err == nil || !strings.Contains(err.Error(), "currencies") {
		t.Fatalf("want a currency mismatch error, got %v", err)
	}
}

// TestPricesTLDWithoutOneYearRenewal checks that a TLD Gandi renews for two
// years at least is left out without an item error: retries cannot change it.
func TestPricesTLDWithoutOneYearRenewal(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {File: "testdata/prices_domains.json"},
		pricesKey: {Body: `{"currency":"EUR","products":[
			{"name":".com","process":"renew","prices":[{"duration_unit":"y","min_duration":1,"max_duration":10,"price_before_taxes":11.5}]},
			{"name":".net","process":"renew","prices":[{"duration_unit":"y","min_duration":2,"max_duration":10,"price_before_taxes":30}]},
			{"name":".co.uk","process":"renew","prices":[{"duration_unit":"m","min_duration":1,"max_duration":11,"price_before_taxes":1}]}
		]}`},
	}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := priceValues(t, res); len(got) != 2 {
		t.Errorf("prices = %v, want the two .com domains only", got)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0 for TLDs without a one-year renewal", res.ItemErrors)
	}
	if !strings.Contains(logs.String(), "example.net") || !strings.Contains(logs.String(), "example.co.uk") {
		t.Errorf("left-out domains not logged: %s", logs.String())
	}
}

func TestPricesOnlyMultiYearTLDsSucceeds(t *testing.T) {
	p, _ := newPrices(t, "", map[string]testkit.Route{
		pricesListKey: {File: "testdata/prices_domains.json"},
		pricesKey: {Body: `{"currency":"EUR","products":[
			{"name":".com","process":"renew","prices":[{"duration_unit":"y","min_duration":2,"max_duration":10,"price_before_taxes":20}]},
			{"name":".net","process":"renew","prices":[{"duration_unit":"y","min_duration":2,"max_duration":10,"price_before_taxes":30}]},
			{"name":".co.uk","process":"renew","prices":[{"duration_unit":"y","min_duration":2,"max_duration":10,"price_before_taxes":12}]}
		]}`},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil || len(res.Metrics) != 0 || res.ItemErrors != 0 {
		t.Fatalf("got %d metrics, %d item errors, err %v; want an empty successful poll", len(res.Metrics), res.ItemErrors, err)
	}
}
