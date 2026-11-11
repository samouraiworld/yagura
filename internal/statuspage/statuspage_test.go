package statuspage

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/testkit"
)

const (
	tStatus    = "/api/v2/status.json"
	tIncidents = "/api/v2/incidents/unresolved.json"
	tMaint     = "/api/v2/scheduled-maintenances/active.json"
)

func TestMain(m *testing.M) {
	httpx.SetDialGuard(httpx.LoopbackOnly)
	os.Exit(m.Run())
}

func newPoller(t *testing.T, routes map[string]testkit.Route, logs io.Writer) (poller.Poller, *httptest.Server) {
	t.Helper()
	srv := testkit.NewServer(t, routes)
	client := httpx.New(httpx.Options{Provider: "statuspage", BaseURL: srv.URL})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewPoller("demo", client, time.Minute, logger), srv
}

func fixture(name string) testkit.Route { return testkit.Route{File: "testdata/" + name} }

func html404() testkit.Route {
	return testkit.Route{Status: 404, Body: "<html>not found</html>", Header: http.Header{"Content-Type": {"text/html"}}}
}

func poll(t *testing.T, p poller.Poller) (poller.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.Poll(ctx)
}

func compare(t *testing.T, res poller.Result, want string) {
	t.Helper()
	if err := testutil.CollectAndCompare(testkit.Collector(res.Metrics), strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

const (
	hdrIndicator = "# HELP yagura_statuspage_indicator Page status indicator: none=0, minor=1, major=2, critical=3, unknown=-1.\n# TYPE yagura_statuspage_indicator gauge\n"
	hdrIncidents = "# HELP yagura_statuspage_unresolved_incidents Number of unresolved incidents.\n# TYPE yagura_statuspage_unresolved_incidents gauge\n"
	hdrMaint     = "# HELP yagura_statuspage_active_maintenances Number of active scheduled maintenances.\n# TYPE yagura_statuspage_active_maintenances gauge\n"
	hdrUpdated   = "# HELP yagura_statuspage_updated_timestamp_seconds Unix time of the page's last update.\n# TYPE yagura_statuspage_updated_timestamp_seconds gauge\n"
)

func indicator(v string) string {
	return hdrIndicator + `yagura_statuspage_indicator{provider="demo"} ` + v + "\n"
}

func updated() string {
	return hdrUpdated + `yagura_statuspage_updated_timestamp_seconds{provider="demo"} 1.767319445e+09` + "\n"
}

func TestStatuspageFull(t *testing.T) {
	p, _ := newPoller(t, map[string]testkit.Route{
		tStatus:    fixture("status_major.json"),
		tIncidents: fixture("incidents_two.json"),
		tMaint:     fixture("maintenances_one.json"),
	}, io.Discard)
	if p.Name() != "statuspage_demo" || p.Interval() != time.Minute {
		t.Errorf("Name/Interval = %q/%v", p.Name(), p.Interval())
	}
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d", res.ItemErrors)
	}
	compare(t, res, indicator("2")+
		hdrIncidents+`yagura_statuspage_unresolved_incidents{provider="demo"} 2`+"\n"+
		hdrMaint+`yagura_statuspage_active_maintenances{provider="demo"} 1`+"\n"+
		updated())
}

func TestStatuspageOptionalEndpointsMissing(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newPoller(t, map[string]testkit.Route{
		tStatus:    fixture("status_none.json"),
		tIncidents: html404(),
		tMaint:     html404(),
	}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, res, indicator("0")+updated())
	if !strings.Contains(logs.String(), "level=DEBUG") {
		t.Errorf("omission not logged at debug:\n%s", logs.String())
	}
}

func TestStatuspageNonJSONOptionalEndpointIsOmitted(t *testing.T) {
	p, _ := newPoller(t, map[string]testkit.Route{
		tStatus:    fixture("status_none.json"),
		tIncidents: {Body: "<html>hello</html>", Header: http.Header{"Content-Type": {"text/html"}}},
		tMaint:     fixture("maintenances_one.json"),
	}, io.Discard)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, res, indicator("0")+
		hdrMaint+`yagura_statuspage_active_maintenances{provider="demo"} 1`+"\n"+
		updated())
}

func TestStatuspageUnknownIndicator(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newPoller(t, map[string]testkit.Route{
		tStatus:    fixture("status_maintenance.json"),
		tIncidents: html404(),
		tMaint:     html404(),
	}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, res, indicator("-1")+updated())
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("unknown indicator not warned:\n%s", logs.String())
	}
}

func TestStatuspageMissingIndicator(t *testing.T) {
	p, _ := newPoller(t, map[string]testkit.Route{
		tStatus:    {Body: `{"page":{"updated_at":"2026-01-02T03:04:05.000+01:00"},"status":{}}`},
		tIncidents: html404(),
		tMaint:     html404(),
	}, io.Discard)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, res, indicator("-1")+updated())
}

func TestStatuspageMissingUpdatedAtOmitsOnlyTimestamp(t *testing.T) {
	for name, body := range map[string]string{
		"absent":      `{"page":{},"status":{"indicator":"minor"}}`,
		"unparseable": `{"page":{"updated_at":"yesterday"},"status":{"indicator":"minor"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			p, _ := newPoller(t, map[string]testkit.Route{
				tStatus:    {Body: body},
				tIncidents: fixture("incidents_two.json"),
				tMaint:     html404(),
			}, &logs)
			res, err := poll(t, p)
			if err != nil {
				t.Fatal(err)
			}
			compare(t, res, indicator("1")+
				hdrIncidents+`yagura_statuspage_unresolved_incidents{provider="demo"} 2`+"\n")
			if !strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("missing updated_at not warned:\n%s", logs.String())
			}
		})
	}
}

func TestStatuspageStatusFails(t *testing.T) {
	p, srv := newPoller(t, map[string]testkit.Route{
		tStatus:    {Status: 500, Body: "boom", Header: http.Header{"Retry-After": {"0"}}},
		tIncidents: fixture("incidents_two.json"),
		tMaint:     fixture("maintenances_one.json"),
	}, io.Discard)
	if _, err := poll(t, p); err == nil {
		t.Fatal("want an error")
	}
	for _, k := range []string{tIncidents, tMaint} {
		if n := testkit.Hits(srv, k); n != 0 {
			t.Errorf("%s requested %d times after status.json failed", k, n)
		}
	}
}

func TestStatuspageOptionalEndpointFailureFailsPoll(t *testing.T) {
	cases := map[string]map[string]testkit.Route{
		"incidents 500": {
			tStatus:    fixture("status_none.json"),
			tIncidents: {Status: 500, Body: "boom", Header: http.Header{"Retry-After": {"0"}}},
			tMaint:     fixture("maintenances_one.json"),
		},
		"maintenances 500": {
			tStatus:    fixture("status_none.json"),
			tIncidents: fixture("incidents_two.json"),
			tMaint:     {Status: 500, Body: "boom", Header: http.Header{"Retry-After": {"0"}}},
		},
		"incidents without array": {
			tStatus:    fixture("status_none.json"),
			tIncidents: {Body: `{"page":{}}`},
			tMaint:     fixture("maintenances_one.json"),
		},
		"incidents null array": {
			tStatus:    fixture("status_none.json"),
			tIncidents: {Body: `{"incidents":null}`},
			tMaint:     fixture("maintenances_one.json"),
		},
		"maintenances without array": {
			tStatus:    fixture("status_none.json"),
			tIncidents: fixture("incidents_two.json"),
			tMaint:     {Body: `{}`},
		},
	}
	for name, routes := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := newPoller(t, routes, io.Discard)
			if _, err := poll(t, p); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestStatuspageEmptyArraysAreZero(t *testing.T) {
	p, _ := newPoller(t, map[string]testkit.Route{
		tStatus:    fixture("status_none.json"),
		tIncidents: {Body: `{"incidents":[]}`},
		tMaint:     {Body: `{"scheduled_maintenances":[]}`},
	}, io.Discard)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, res, indicator("0")+
		hdrIncidents+`yagura_statuspage_unresolved_incidents{provider="demo"} 0`+"\n"+
		hdrMaint+`yagura_statuspage_active_maintenances{provider="demo"} 0`+"\n"+
		updated())
}

func TestStatuspageItemErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing indicator", `{"page":{"updated_at":"2026-01-02T03:04:05.000+01:00"},"status":{}}`, 1},
		{"empty indicator", `{"page":{"updated_at":"2026-01-02T03:04:05.000+01:00"},"status":{"indicator":""}}`, 1},
		{"missing updated_at", `{"page":{},"status":{"indicator":"none"}}`, 1},
		{"unparseable updated_at", `{"page":{"updated_at":"yesterday"},"status":{"indicator":"none"}}`, 1},
		{"both missing", `{"page":{},"status":{}}`, 2},
		{"unknown non-empty indicator", `{"page":{"updated_at":"2026-01-02T03:04:05.000+01:00"},"status":{"indicator":"maintenance"}}`, 1},
		{"full", `{"page":{"updated_at":"2026-01-02T03:04:05.000+01:00"},"status":{"indicator":"none"}}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newPoller(t, map[string]testkit.Route{
				tStatus:    {Body: tc.body},
				tIncidents: html404(),
				tMaint:     html404(),
			}, io.Discard)
			res, err := poll(t, p)
			if err != nil {
				t.Fatal(err)
			}
			if res.ItemErrors != tc.want {
				t.Errorf("ItemErrors = %d, want %d", res.ItemErrors, tc.want)
			}
		})
	}
}

func TestStatuspageWarningsAreBounded(t *testing.T) {
	longIndicator := strings.Repeat("i", 5000)
	longUpdated := "u" + strings.Repeat("v", 5000)
	var logs bytes.Buffer
	p, _ := newPoller(t, map[string]testkit.Route{
		tStatus:    {Body: `{"page":{"updated_at":"` + longUpdated + `"},"status":{"indicator":"` + longIndicator + `"}}`},
		tIncidents: html404(),
		tMaint:     html404(),
	}, &logs)
	if _, err := poll(t, p); err != nil {
		t.Fatal(err)
	}
	if n := len(logs.String()); n > 1000 {
		t.Errorf("log output is %d bytes; remote strings must be capped", n)
	}
	var updatedLine string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "not RFC 3339") {
			updatedLine = l
		}
	}
	if !strings.Contains(updatedLine, "updated_at="+longUpdated[:maxLogValue]) || strings.Contains(updatedLine, strings.Repeat("v", maxLogValue+1)) {
		t.Errorf("updated_at warning should carry the capped updated_at value:\n%s", updatedLine)
	}
}

func TestStatuspageOptionalEndpointShapes(t *testing.T) {
	omit := indicator("0") + updated()
	cases := []struct {
		name    string
		body    string
		want    string // expected metrics when the poll succeeds
		wantErr bool
	}{
		{"top-level array is not statuspage-shaped", `[]`, omit, false},
		{"top-level string", `"none"`, omit, false},
		{"top-level null", `null`, omit, false},
		{"array of non-objects counts its length", `{"incidents":[1,"x"]}`, omit + hdrIncidents + `yagura_statuspage_unresolved_incidents{provider="demo"} 2` + "\n", false},
		{"empty array", `{"incidents":[]}`, omit + hdrIncidents + `yagura_statuspage_unresolved_incidents{provider="demo"} 0` + "\n", false},
		{"empty object", `{}`, "", true},
		{"null array", `{"incidents":null}`, "", true},
		{"object instead of array", `{"incidents":{}}`, "", true},
		{"string instead of array", `{"incidents":"none"}`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			p, _ := newPoller(t, map[string]testkit.Route{
				tStatus:    fixture("status_none.json"),
				tIncidents: {Body: tc.body},
				tMaint:     html404(),
			}, &logs)
			res, err := poll(t, p)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			compare(t, res, tc.want)
		})
	}
}
