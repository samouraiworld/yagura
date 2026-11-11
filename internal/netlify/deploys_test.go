package netlify

import (
	"bytes"
	"context"
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
	id1 = "00000000-0000-0000-0000-000000000001"
	id2 = "00000000-0000-0000-0000-000000000002"

	sitesKey1   = "GET /api/v1/sites?filter=all&page=1&per_page=100"
	sitesKey2   = "GET /api/v1/sites?filter=all&page=2&per_page=100"
	deploysKey1 = "GET /api/v1/sites/" + id1 + "/deploys?per_page=1&production=true"
	deploysKey2 = "GET /api/v1/sites/" + id2 + "/deploys?per_page=1&production=true"
)

var nextLink = http.Header{"Link": {`<https://api.example.invalid/api/v1/sites?page=2>; rel="next"`}}

func TestMain(m *testing.M) {
	httpx.SetDialGuard(httpx.LoopbackOnly)
	os.Exit(m.Run())
}

func newPoller(t *testing.T, routes map[string]testkit.Route, logs *bytes.Buffer) (poller.Poller, *httptest.Server) {
	t.Helper()
	srv := testkit.NewServer(t, routes)
	client := httpx.New(httpx.Options{Provider: "netlify", BaseURL: srv.URL})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewDeploysPoller(client, time.Minute, logger), srv
}

func poll(t *testing.T, p poller.Poller) (poller.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.Poll(ctx)
}

// values returns metric name -> label values joined by "|" (sorted by label name) -> value.
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
			var parts []string
			for _, l := range m.GetLabel() {
				parts = append(parts, l.GetValue())
			}
			out[f.GetName()][strings.Join(parts, "|")] = m.GetGauge().GetValue()
		}
	}
	return out
}

func twoSites() map[string]testkit.Route {
	return map[string]testkit.Route{
		sitesKey1:   {File: "testdata/sites_page1.json", Header: nextLink},
		sitesKey2:   {File: "testdata/sites_page2.json"},
		deploysKey1: {File: "testdata/deploy_ready.json"},
		deploysKey2: {File: "testdata/deploy_error.json"},
	}
}

func TestDeploysStates(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newPoller(t, twoSites(), &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d", res.ItemErrors)
	}
	v := values(t, res)
	if got := v["yagura_netlify_last_deploy_info"]["demo-team|demo-site|ready"]; got != 1 {
		t.Errorf("info demo-site = %v, want 1", got)
	}
	if got := v["yagura_netlify_last_deploy_info"]["demo-team|demo-docs|error"]; got != 1 {
		t.Errorf("info demo-docs = %v, want 1", got)
	}
	failed := v["yagura_netlify_last_deploy_failed"]
	if failed["demo-site"] != 0 || failed["demo-docs"] != 1 || len(failed) != 2 {
		t.Errorf("failed = %v", failed)
	}
	created := v["yagura_netlify_last_deploy_created_timestamp_seconds"]
	if created["demo-site"] != 1767323045 || created["demo-docs"] != 1767409445 {
		t.Errorf("created = %v", created)
	}
}

func TestDeploysPagination(t *testing.T) {
	var logs bytes.Buffer
	p, srv := newPoller(t, twoSites(), &logs)
	if _, err := poll(t, p); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if n := testkit.Hits(srv, sitesKey1) + testkit.Hits(srv, sitesKey2); n != 2 {
		t.Errorf("site requests = %d, want 2", n)
	}
}

func TestSitesMaxPages(t *testing.T) {
	old := httpx.MaxPages
	httpx.MaxPages = 5
	t.Cleanup(func() { httpx.MaxPages = old })
	routes := map[string]testkit.Route{"/api/v1/sites": {File: "testdata/sites_page1.json", Header: nextLink}}
	var logs bytes.Buffer
	p, srv := newPoller(t, routes, &logs)
	if _, err := poll(t, p); err == nil {
		t.Fatal("expected an error")
	}
	if n := testkit.Hits(srv, "/api/v1/sites"); n != 5 {
		t.Errorf("site requests = %d, want 5", n)
	}
}

func TestSitesEmptyPageStops(t *testing.T) {
	routes := map[string]testkit.Route{
		sitesKey1:   {File: "testdata/sites_page1.json", Header: nextLink},
		sitesKey2:   {Body: "[]", Header: nextLink},
		deploysKey1: {File: "testdata/deploy_ready.json"},
	}
	var logs bytes.Buffer
	p, srv := newPoller(t, routes, &logs)
	if _, err := poll(t, p); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if n := testkit.Hits(srv, sitesKey2); n != 1 {
		t.Errorf("page 2 requests = %d", n)
	}
}

func TestDeploysNoProductionDeploy(t *testing.T) {
	routes := map[string]testkit.Route{
		sitesKey1:   {File: "testdata/sites_page1.json"},
		deploysKey1: {Body: "[]"},
	}
	var logs bytes.Buffer
	p, _ := newPoller(t, routes, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if res.ItemErrors != 0 || len(res.Metrics) != 0 {
		t.Errorf("ItemErrors = %d, metrics = %d", res.ItemErrors, len(res.Metrics))
	}
}

func TestDeploysEmpty(t *testing.T) {
	routes := map[string]testkit.Route{sitesKey1: {File: "testdata/sites_empty.json"}}
	var logs bytes.Buffer
	p, _ := newPoller(t, routes, &logs)
	res, err := poll(t, p)
	if err != nil || len(res.Metrics) != 0 || res.ItemErrors != 0 {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}

func TestSitesNotAnArrayFailsPoll(t *testing.T) {
	for _, body := range []string{"null", `{"a":1}`} {
		routes := map[string]testkit.Route{sitesKey1: {Body: body}}
		var logs bytes.Buffer
		p, _ := newPoller(t, routes, &logs)
		if _, err := poll(t, p); err == nil {
			t.Errorf("body %s: expected an error", body)
		}
	}
}

func TestSitesSkippedAndDeduped(t *testing.T) {
	routes := map[string]testkit.Route{
		sitesKey1: {Body: `[
			{"id":"` + id1 + `","name":"demo-site","account_slug":"demo-team"},
			{"id":"../x","name":"bad-id","account_slug":"demo-team"},
			{"id":"","name":"no-id"},
			{"id":"abc","name":""},
			{"id":"` + id2 + `","name":"demo-site","account_slug":"demo-team"},
			{"id":"` + id1 + `","name":"demo-site","account_slug":""}
		]`},
		deploysKey1: {File: "testdata/deploy_ready.json"},
	}
	var logs bytes.Buffer
	p, _ := newPoller(t, routes, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	// bad id, empty id, empty name, repeated name = 4.
	if res.ItemErrors != 4 {
		t.Errorf("ItemErrors = %d, want 4", res.ItemErrors)
	}
	// Last occurrence of id1 wins: empty account label.
	if got := values(t, res)["yagura_netlify_last_deploy_info"]["|demo-site|ready"]; got != 1 {
		t.Errorf("info = %v", values(t, res))
	}
}

func TestDeployItemCases(t *testing.T) {
	tests := []struct {
		name       string
		route      testkit.Route
		wantSeries map[string]int
		wantErrors int
		wantErr    bool
	}{
		{"empty state", testkit.Route{Body: `[{"state":"","created_at":"2026-01-02T03:04:05Z"}]`}, nil, 1, false},
		{"missing created_at", testkit.Route{Body: `[{"state":"ready"}]`},
			map[string]int{"yagura_netlify_last_deploy_info": 1, "yagura_netlify_last_deploy_failed": 1}, 1, false},
		{"bad created_at", testkit.Route{Body: `[{"state":"error","created_at":"yesterday"}]`},
			map[string]int{"yagura_netlify_last_deploy_info": 1, "yagura_netlify_last_deploy_failed": 1}, 1, false},
		{"null body", testkit.Route{Body: `null`}, nil, 1, true},
		{"not found", testkit.Route{Status: 404, Body: `{}`}, nil, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			routes := map[string]testkit.Route{sitesKey1: {File: "testdata/sites_page1.json"}, deploysKey1: tc.route}
			var logs bytes.Buffer
			p, _ := newPoller(t, routes, &logs)
			res, err := poll(t, p)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if res.ItemErrors != tc.wantErrors {
				t.Errorf("ItemErrors = %d, want %d", res.ItemErrors, tc.wantErrors)
			}
			v := values(t, res)
			for name, n := range tc.wantSeries {
				if len(v[name]) != n {
					t.Errorf("%s series = %d, want %d", name, len(v[name]), n)
				}
			}
			if tc.wantSeries == nil && len(res.Metrics) != 0 {
				t.Errorf("metrics = %d, want 0", len(res.Metrics))
			}
			if len(v["yagura_netlify_last_deploy_created_timestamp_seconds"]) != 0 {
				t.Errorf("created series must be omitted")
			}
		})
	}
}

func TestDeployAuthErrorFailsPoll(t *testing.T) {
	routes := map[string]testkit.Route{
		sitesKey1:   {File: "testdata/sites_page1.json"},
		deploysKey1: {Status: 401, Body: `{}`},
	}
	var logs bytes.Buffer
	p, _ := newPoller(t, routes, &logs)
	_, err := poll(t, p)
	if err == nil || !httpx.IsAuthError(err) {
		t.Errorf("err = %v, want auth error", err)
	}
}

func TestHasNextLink(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   bool
	}{
		{"none", nil, false},
		{"next", []string{`<https://x.invalid/?page=2>; rel="next"`}, true},
		{"unquoted", []string{`<https://x.invalid/?page=2>; rel=next`}, true},
		{"uppercase", []string{`<https://x.invalid/?page=2>; REL="NEXT"`}, true},
		{"prev only", []string{`<https://x.invalid/?page=1>; rel="prev"`}, false},
		{"comma separated", []string{`<https://x.invalid/?page=1>; rel="prev", <https://x.invalid/?page=3>; rel="next"`}, true},
		{"several values", []string{`<a>; rel="first"`, `<b>; rel="next"`}, true},
		{"multi rel", []string{`<a>; rel="prev next"`}, true},
		{"last only", []string{`<a>; rel="last"`}, false},
		{"garbage", []string{`next`}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tc.values {
				h.Add("Link", v)
			}
			if got := hasNextLink(h); got != tc.want {
				t.Errorf("hasNextLink = %v, want %v", got, tc.want)
			}
		})
	}
}
