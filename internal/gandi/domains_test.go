package gandi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/testkit"
)

const (
	listPath = "/v5/domain/domains"
	page1Key = "GET " + listPath + "?page=1&per_page=100"
	page2Key = "GET " + listPath + "?page=2&per_page=100"
)

func TestMain(m *testing.M) {
	httpx.SetDialGuard(httpx.LoopbackOnly)
	os.Exit(m.Run())
}

func newPoller(t *testing.T, sharingID string, routes map[string]testkit.Route, logs *bytes.Buffer) (poller.Poller, *httptest.Server) {
	t.Helper()
	srv := testkit.NewServer(t, routes)
	client := httpx.New(httpx.Options{Provider: "gandi", BaseURL: srv.URL})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewDomainsPoller(client, sharingID, time.Minute, logger), srv
}

func poll(t *testing.T, p poller.Poller) (poller.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.Poll(ctx)
}

// hundred builds a 100-item page d000.example.com ... d099.example.com from
// the first entry of domains_page1.json.
func hundred(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/domains_page1.json")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl []map[string]any
	if err := json.Unmarshal(raw, &tmpl); err != nil || len(tmpl) == 0 {
		t.Fatalf("template: %v", err)
	}
	items := make([]map[string]any, 100)
	for i := range items {
		e := make(map[string]any, len(tmpl[0]))
		for k, v := range tmpl[0] {
			e[k] = v
		}
		e["fqdn"] = fmt.Sprintf("d%03d.example.com", i)
		items[i] = e
	}
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// values gathers the metrics through a pedantic registry (which rejects
// duplicate series) and returns metric name -> domain -> value.
func values(t *testing.T, res poller.Result) map[string]map[string]float64 {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(testkit.Collector(res.Metrics))
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]map[string]float64{}
	for _, f := range fams {
		out[f.GetName()] = map[string]float64{}
		for _, m := range f.GetMetric() {
			out[f.GetName()][m.GetLabel()[0].GetValue()] = m.GetGauge().GetValue()
		}
	}
	return out
}

func TestDomainsTwoPages(t *testing.T) {
	p, _ := newPoller(t, "", map[string]testkit.Route{
		page1Key: {Body: hundred(t)},
		page2Key: {File: "testdata/domains_page2.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res)
	if got := len(v[MetricNames[0]]); got != 102 {
		t.Errorf("expiry series = %d, want 102", got)
	}
	if got := len(v[MetricNames[1]]); got != 102 {
		t.Errorf("autorenew series = %d, want 102", got)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
	}
	if got := v[MetricNames[0]]["example.net"]; got != 1.802513058e+09 {
		t.Errorf("example.net expiry = %v, want 1.802513058e+09", got)
	}
	if got, ok := v[MetricNames[1]]["example.net"]; !ok || got != 0 {
		t.Errorf("example.net autorenew = %v (present %v), want 0", got, ok)
	}
	if got := v[MetricNames[1]]["www.example.org"]; got != 1 {
		t.Errorf("www.example.org autorenew = %v, want 1", got)
	}
}

func TestDomainsDedupAcrossPages(t *testing.T) {
	// Page 2 repeats d099.example.com (the listing shifted between requests)
	// with other values; the last occurrence wins and one series remains.
	p, _ := newPoller(t, "", map[string]testkit.Route{
		page1Key: {Body: hundred(t)},
		page2Key: {File: "testdata/domains_shifted.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res) // fails on duplicate series
	if got := len(v[MetricNames[0]]); got != 101 {
		t.Errorf("expiry series = %d, want 101", got)
	}
	if got := v[MetricNames[0]]["d099.example.com"]; got != 1893456000 {
		t.Errorf("d099 expiry = %v, want 1893456000 (last wins)", got)
	}
	if got := v[MetricNames[1]]["d099.example.com"]; got != 0 {
		t.Errorf("d099 autorenew = %v, want 0 (last wins)", got)
	}
}

func TestDomainsStopsAtTotalCount(t *testing.T) {
	p, srv := newPoller(t, "", map[string]testkit.Route{
		listPath: {Body: hundred(t), Header: http.Header{"Total-Count": {"100"}}},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := testkit.Hits(srv, listPath); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
	if got := len(values(t, res)[MetricNames[0]]); got != 100 {
		t.Errorf("expiry series = %d, want 100", got)
	}
}

func TestDomainsMaxPages(t *testing.T) {
	old := httpx.MaxPages
	httpx.MaxPages = 3
	t.Cleanup(func() { httpx.MaxPages = old })
	p, srv := newPoller(t, "", map[string]testkit.Route{
		listPath: {Body: hundred(t)},
	}, &bytes.Buffer{})
	if _, err := poll(t, p); err == nil {
		t.Fatal("expected an error after MaxPages pages")
	}
	if got := testkit.Hits(srv, listPath); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
}

func TestDomainsMissingExpiry(t *testing.T) {
	body := `[
	  {"fqdn": "ok.example.com", "autorenew": true, "dates": {"registry_ends_at": "2027-01-01T00:00:00Z"}},
	  {"fqdn": "nodates.example.com", "autorenew": true}
	]`
	var logs bytes.Buffer
	p, _ := newPoller(t, "", map[string]testkit.Route{page1Key: {Body: body}}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res)
	if _, ok := v[MetricNames[0]]["nodates.example.com"]; ok {
		t.Error("expiry series exists for a domain without registry_ends_at")
	}
	if _, ok := v[MetricNames[1]]["nodates.example.com"]; ok {
		t.Error("autorenew series exists for a skipped domain")
	}
	if len(v[MetricNames[0]]) != 1 || len(v[MetricNames[1]]) != 1 {
		t.Errorf("want only ok.example.com, got %v", v)
	}
	if res.ItemErrors != 1 {
		t.Errorf("ItemErrors = %d, want 1", res.ItemErrors)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("no warn log: %s", logs.String())
	}
}

func TestDomainsSkipRules(t *testing.T) {
	body := `[
	  {"fqdn": "empty-date.example.com", "autorenew": true, "dates": {"registry_ends_at": ""}},
	  {"fqdn": "bad-date.example.com", "autorenew": true, "dates": {"registry_ends_at": "next year"}},
	  {"fqdn": "", "autorenew": true, "dates": {"registry_ends_at": "2027-01-01T00:00:00Z"}},
	  {"fqdn": "no-renew.example.com", "dates": {"registry_ends_at": "2027-01-01T00:00:00Z"}},
	  {"fqdn": "null-renew.example.com", "autorenew": null, "dates": {"registry_ends_at": "2027-01-01T00:00:00Z"}},
	  {"fqdn": "good.example.com", "autorenew": true, "dates": {"registry_ends_at": "2027-01-01T00:00:00Z"}}
	]`
	p, _ := newPoller(t, "", map[string]testkit.Route{page1Key: {Body: body}}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res)
	if got := len(v[MetricNames[0]]); got != 3 {
		t.Errorf("expiry series = %d, want 3 (no-renew, null-renew, good): %v", got, v)
	}
	if got := len(v[MetricNames[1]]); got != 1 {
		t.Errorf("autorenew series = %d, want 1 (good): %v", got, v)
	}
	if res.ItemErrors != 5 {
		t.Errorf("ItemErrors = %d, want 5", res.ItemErrors)
	}
}

func TestDomainsEmpty(t *testing.T) {
	p, _ := newPoller(t, "", map[string]testkit.Route{
		page1Key: {File: "testdata/domains_empty.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Metrics) != 0 || res.ItemErrors != 0 {
		t.Errorf("got %d metrics, %d item errors; want none", len(res.Metrics), res.ItemErrors)
	}
}

func TestDomainsSharingID(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	key := page1Key + "&sharing_id=" + id
	p, srv := newPoller(t, id, map[string]testkit.Route{
		key: {File: "testdata/domains_empty.json"},
	}, &bytes.Buffer{})
	if _, err := poll(t, p); err != nil {
		t.Fatal(err)
	}
	if got := testkit.Hits(srv, key); got != 1 {
		t.Errorf("requests with sharing_id = %d, want 1", got)
	}
}

func TestDomainsFailures(t *testing.T) {
	cases := map[string]testkit.Route{
		"http error":  {Status: 500, Body: `{"message":"boom"}`},
		"not json":    {Body: "<html>"},
		"not array":   {Body: `{"fqdn":"a.example.com"}`},
		"null":        {Body: "null"},
		"empty body":  {Body: " "},
		"string body": {Body: `"x"`},
	}
	for name, route := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := newPoller(t, "", map[string]testkit.Route{page1Key: route}, &bytes.Buffer{})
			if _, err := poll(t, p); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	t.Run("decode error kind", func(t *testing.T) {
		p, _ := newPoller(t, "", map[string]testkit.Route{page1Key: {Body: `{}`}}, &bytes.Buffer{})
		if _, err := poll(t, p); !errors.Is(err, httpx.ErrDecode) {
			t.Errorf("err = %v, want ErrDecode", err)
		}
	})
}

func TestDomainsName(t *testing.T) {
	p, _ := newPoller(t, "", nil, &bytes.Buffer{})
	if p.Name() != "gandi_domains" || p.Interval() != time.Minute {
		t.Errorf("name %q interval %v", p.Name(), p.Interval())
	}
}

func TestDomainsIgnoresNonPositiveTotalCount(t *testing.T) {
	for _, total := range []string{"0", "-1"} {
		t.Run(total, func(t *testing.T) {
			p, srv := newPoller(t, "", map[string]testkit.Route{
				page1Key: {Body: hundred(t), Header: http.Header{"Total-Count": {total}}},
				page2Key: {File: "testdata/domains_page2.json"},
			}, &bytes.Buffer{})
			res, err := poll(t, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{page1Key, page2Key} {
				if got := testkit.Hits(srv, key); got != 1 {
					t.Errorf("%s requests = %d, want 1", key, got)
				}
			}
			if got := len(values(t, res)[MetricNames[0]]); got != 102 {
				t.Errorf("expiry series = %d, want 102", got)
			}
		})
	}
}
