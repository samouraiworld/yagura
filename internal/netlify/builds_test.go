package netlify

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/testkit"
)

const (
	acct1 = "00000000-0000-0000-0000-0000000000a1"
	acct2 = "00000000-0000-0000-0000-0000000000a2"

	accountsKey = "GET /api/v1/accounts"
	buildsKey1  = "GET /api/v1/" + acct1 + "/builds/status"
	buildsKey2  = "GET /api/v1/" + acct2 + "/builds/status"
)

func newBuildsPoller(t *testing.T, routes map[string]testkit.Route, logs *bytes.Buffer) (poller.Poller, *httptest.Server) {
	t.Helper()
	srv := testkit.NewServer(t, routes)
	client := httpx.New(httpx.Options{Provider: "netlify", BaseURL: srv.URL})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewBuildsPoller(client, time.Minute, logger), srv
}

func oneAccount(build testkit.Route) map[string]testkit.Route {
	return map[string]testkit.Route{accountsKey: {File: "testdata/accounts.json"}, buildsKey1: build}
}

func TestBuildMinutes(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newBuildsPoller(t, oneAccount(testkit.Route{File: "testdata/builds_status.json"}), &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
	}
	v := values(t, res)
	end := float64(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).Unix())
	want := map[string]float64{
		"yagura_netlify_build_minutes_used":                 120,
		"yagura_netlify_build_minutes_included":             300,
		"yagura_netlify_build_period_end_timestamp_seconds": end,
	}
	for name, w := range want {
		if got, ok := v[name]["demo-team"]; !ok || got != w || len(v[name]) != 1 {
			t.Errorf("%s = %v, want {demo-team: %v}", name, v[name], w)
		}
	}
}

func TestBuildMinutesIncludedUnparsable(t *testing.T) {
	cases := map[string]testkit.Route{
		"empty":     {File: "testdata/builds_status_empty_included.json"},
		"unlimited": {Body: `[{"minutes":{"current":120,"included_minutes":"unlimited","period_end_date":"2026-02-01T00:00:00Z"}}]`},
		"negative":  {Body: `[{"minutes":{"current":120,"included_minutes":"-5","period_end_date":"2026-02-01T00:00:00Z"}}]`},
		"nan":       {Body: `[{"minutes":{"current":120,"included_minutes":"NaN","period_end_date":"2026-02-01T00:00:00Z"}}]`},
		"null":      {Body: `[{"minutes":{"current":120,"included_minutes":null,"period_end_date":"2026-02-01T00:00:00Z"}}]`},
		"absent":    {Body: `[{"minutes":{"current":120,"period_end_date":"2026-02-01T00:00:00Z"}}]`},
		"negnum":    {Body: `[{"minutes":{"current":120,"included_minutes":-5,"period_end_date":"2026-02-01T00:00:00Z"}}]`},
		"inf":       {Body: `[{"minutes":{"current":120,"included_minutes":"+Inf","period_end_date":"2026-02-01T00:00:00Z"}}]`},
	}
	for name, route := range cases {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			p, _ := newBuildsPoller(t, oneAccount(route), &logs)
			res, err := poll(t, p)
			if err != nil {
				t.Fatalf("poll: %v", err)
			}
			v := values(t, res)
			if res.ItemErrors != 0 {
				t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
			}
			if len(v["yagura_netlify_build_minutes_included"]) != 0 {
				t.Errorf("included series must be omitted")
			}
			if len(v["yagura_netlify_build_minutes_used"]) != 1 || len(v["yagura_netlify_build_period_end_timestamp_seconds"]) != 1 {
				t.Errorf("used and period series must remain: %v", v)
			}
		})
	}
}

func TestBuildMinutesIncludedNumeric(t *testing.T) {
	route := testkit.Route{Body: `[{"minutes":{"current":120,"included_minutes":300,"period_end_date":"2026-02-01T00:00:00Z"}}]`}
	var logs bytes.Buffer
	p, _ := newBuildsPoller(t, oneAccount(route), &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
	}
	if got := values(t, res)["yagura_netlify_build_minutes_included"]["demo-team"]; got != 300 {
		t.Errorf("included = %v, want 300", got)
	}
}

func TestBuildStatusEmptyArray(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newBuildsPoller(t, oneAccount(testkit.Route{Body: `[]`}), &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(res.Metrics) != 0 || res.ItemErrors != 0 {
		t.Errorf("metrics = %d, ItemErrors = %d, want 0, 0", len(res.Metrics), res.ItemErrors)
	}
}

func TestBuildStatusItemCases(t *testing.T) {
	tests := []struct {
		name       string
		route      testkit.Route
		wantSeries map[string]int
		wantErrors int
	}{
		{"no minutes object", testkit.Route{Body: `[{"active":true}]`}, nil, 1},
		{"missing current", testkit.Route{Body: `[{"minutes":{"included_minutes":"300","period_end_date":"2026-02-01T00:00:00Z"}}]`},
			map[string]int{"yagura_netlify_build_minutes_included": 1, "yagura_netlify_build_period_end_timestamp_seconds": 1}, 1},
		{"missing period", testkit.Route{Body: `[{"minutes":{"current":1,"included_minutes":"300"}}]`},
			map[string]int{"yagura_netlify_build_minutes_used": 1, "yagura_netlify_build_minutes_included": 1}, 1},
		{"bad period", testkit.Route{Body: `[{"minutes":{"current":1,"included_minutes":"300","period_end_date":"soon"}}]`},
			map[string]int{"yagura_netlify_build_minutes_used": 1, "yagura_netlify_build_minutes_included": 1}, 1},
		{"null body", testkit.Route{Body: `null`}, nil, 1},
		{"not found", testkit.Route{Status: 404, Body: `{}`}, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			routes := map[string]testkit.Route{
				accountsKey: {Body: `[{"id":"` + acct1 + `","slug":"demo-team"},{"id":"` + acct2 + `","slug":"demo-docs"}]`},
				buildsKey1:  tc.route,
				buildsKey2:  {File: "testdata/builds_status.json"},
			}
			var logs bytes.Buffer
			p, _ := newBuildsPoller(t, routes, &logs)
			res, err := poll(t, p)
			if err != nil {
				t.Fatalf("poll: %v", err)
			}
			if res.ItemErrors != tc.wantErrors {
				t.Errorf("ItemErrors = %d, want %d", res.ItemErrors, tc.wantErrors)
			}
			v := values(t, res)
			for _, name := range MetricNames[3:] {
				want := 1 // the healthy second account
				if n, ok := tc.wantSeries[name]; ok {
					want += n
				}
				if len(v[name]) != want {
					t.Errorf("%s series = %d, want %d", name, len(v[name]), want)
				}
			}
		})
	}
}

func TestAccountsSkippedAndDeduped(t *testing.T) {
	routes := map[string]testkit.Route{
		accountsKey: {Body: `[
			{"id":"` + acct1 + `","slug":"demo-team"},
			{"id":"../x","slug":"bad"},
			{"id":"` + acct2 + `","slug":""},
			{"id":"` + acct1 + `","slug":"demo-team"},
			{"id":"00000000-0000-0000-0000-0000000000a3","slug":"demo-team"}]`},
		buildsKey1: {File: "testdata/builds_status.json"},
	}
	var logs bytes.Buffer
	p, srv := newBuildsPoller(t, routes, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if res.ItemErrors != 3 {
		t.Errorf("ItemErrors = %d, want 3 (bad id, empty slug, repeated slug)", res.ItemErrors)
	}
	if n := testkit.Hits(srv, buildsKey1); n != 1 {
		t.Errorf("builds hits = %d, want 1", n)
	}
	if len(values(t, res)["yagura_netlify_build_minutes_used"]) != 1 {
		t.Errorf("want exactly one used series")
	}
}

func TestAccountsNotAnArrayFailsPoll(t *testing.T) {
	for _, body := range []string{`null`, `{}`} {
		var logs bytes.Buffer
		p, _ := newBuildsPoller(t, map[string]testkit.Route{accountsKey: {Body: body}}, &logs)
		if _, err := poll(t, p); err == nil {
			t.Errorf("body %s: want error", body)
		}
	}
}

func TestBuildsAuthErrorFailsPoll(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newBuildsPoller(t, oneAccount(testkit.Route{Status: 401, Body: `{}`}), &logs)
	if _, err := poll(t, p); err == nil {
		t.Fatal("want error on 401")
	}
}
