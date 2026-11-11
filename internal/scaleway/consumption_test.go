package scaleway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/testkit"
)

const (
	tOrg  = "00000000-0000-0000-0000-000000000009"
	tPath = "/billing/v2beta1/consumptions"
)

func TestMain(m *testing.M) {
	httpx.SetDialGuard(httpx.LoopbackOnly)
	os.Exit(m.Run())
}

// pageKey is the testkit route key of one consumptions page.
func pageKey(n string) string {
	return tPath + "?organization_id=" + tOrg + "&page=" + n + "&page_size=100"
}

func newPoller(t *testing.T, routes map[string]testkit.Route, logs io.Writer) (poller.Poller, *httptest.Server) {
	t.Helper()
	srv := testkit.NewServer(t, routes)
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewConsumptionPoller(client, tOrg, time.Hour, logger), srv
}

func fixture(name string) testkit.Route { return testkit.Route{File: "testdata/" + name} }

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
	hdrAmount  = "# HELP yagura_scaleway_consumption_amount Month-to-date consumption per project and category, as reported by the Scaleway billing API.\n# TYPE yagura_scaleway_consumption_amount gauge\n"
	hdrUpdated = "# HELP yagura_scaleway_consumption_updated_timestamp_seconds Unix time of the billing API's last consumption update.\n# TYPE yagura_scaleway_consumption_updated_timestamp_seconds gauge\n"
	updated    = hdrUpdated + "yagura_scaleway_consumption_updated_timestamp_seconds 1.7938674e+09\n"
)

func amount(projectID, projectName, category, v string) string {
	return `yagura_scaleway_consumption_amount{category="` + category + `",currency="EUR",project_id="` +
		projectID + `",project_name="` + projectName + `"} ` + v + "\n"
}

func TestConsumptionAggregates(t *testing.T) {
	p, srv := newPoller(t, map[string]testkit.Route{
		pageKey("1"): fixture("consumptions_page1.json"),
		pageKey("2"): fixture("consumptions_page2.json"),
	}, io.Discard)
	if p.Name() != "scaleway_consumption" || p.Interval() != time.Hour {
		t.Errorf("Name/Interval = %q/%v", p.Name(), p.Interval())
	}
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d", res.ItemErrors)
	}
	compare(t, res, hdrAmount+
		amount("00000000-0000-0000-0000-000000000001", "demo-project", "Compute", "4")+
		amount("00000000-0000-0000-0000-000000000001", "demo-project", "Storage", "0.1")+
		amount("00000000-0000-0000-0000-000000000002", "demo-project-2", "Compute", "1")+
		updated)
	for _, n := range []string{"1", "2"} {
		if h := testkit.Hits(srv, pageKey(n)); h != 1 {
			t.Errorf("page %s requested %d times, want 1", n, h)
		}
	}
}

func TestMoneyAmountNegative(t *testing.T) {
	for _, tc := range []struct {
		units int64
		nanos int32
		want  float64
	}{
		{-1, -500000000, -1.5},
		{0, 250000000, 0.25},
		{3, 0, 3},
		{0, -1, -1e-9},
	} {
		if got := moneyAmount(tc.units, tc.nanos); got != tc.want {
			t.Errorf("moneyAmount(%d, %d) = %v, want %v", tc.units, tc.nanos, got, tc.want)
		}
	}
}

// Review Focus 2: a valid token on an account with no consumption.
func TestConsumptionEmpty(t *testing.T) {
	p, srv := newPoller(t, map[string]testkit.Route{
		pageKey("1"): fixture("consumptions_empty.json"),
	}, io.Discard)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Metrics) != 0 || res.ItemErrors != 0 {
		t.Errorf("got %d metrics, %d item errors; want none", len(res.Metrics), res.ItemErrors)
	}
	if h := testkit.Hits(srv, pageKey("1")); h != 1 {
		t.Errorf("requests = %d, want 1", h)
	}
}

func TestConsumptionQuery(t *testing.T) {
	var (
		mu  sync.Mutex
		got []*url.URL
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"consumptions":[],"total_count":0}`)
	}))
	t.Cleanup(srv.Close)
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL})
	p := NewConsumptionPoller(client, tOrg, time.Hour, slog.New(slog.DiscardHandler))
	if _, err := poll(t, p); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("requests = %d, want 1", len(got))
	}
	q := got[0].Query()
	if got[0].Path != tPath || q.Get("organization_id") != tOrg || q.Get("page") != "1" || q.Get("page_size") != "100" {
		t.Errorf("request = %s", got[0].RequestURI())
	}
	if q.Has("billing_period") {
		t.Errorf("billing_period must be omitted (current month): %s", got[0].RequestURI())
	}
}

// Review Focus 5: a server that ignores page and never reaches total_count.
func TestConsumptionMaxPages(t *testing.T) {
	old := httpx.MaxPages
	httpx.MaxPages = 3
	t.Cleanup(func() { httpx.MaxPages = old })
	p, srv := newPoller(t, map[string]testkit.Route{
		tPath: {Body: `{"consumptions":[{"value":{"currency_code":"EUR","units":1},"project_id":"00000000-0000-0000-0000-000000000001","project_name":"demo-project","category_name":"Compute"}],"total_count":100}`},
	}, io.Discard)
	_, err := poll(t, p)
	if err == nil || !strings.Contains(err.Error(), "3 pages") {
		t.Errorf("err = %v, want a max pages error", err)
	}
	if h := testkit.Hits(srv, tPath); h != 3 {
		t.Errorf("requests = %d, want 3", h)
	}
}

func TestConsumptionWithoutTotalCountReadsUntilEmptyPage(t *testing.T) {
	row := `{"value":{"currency_code":"EUR","units":2},"project_id":"00000000-0000-0000-0000-000000000001","project_name":"demo-project","category_name":"Compute"}`
	p, _ := newPoller(t, map[string]testkit.Route{
		pageKey("1"): {Body: `{"consumptions":[` + row + `]}`},
		pageKey("2"): {Body: `{"consumptions":[` + row + `]}`},
		pageKey("3"): {Body: `{"consumptions":[]}`},
	}, io.Discard)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, res, hdrAmount+amount("00000000-0000-0000-0000-000000000001", "demo-project", "Compute", "4"))
}

func TestConsumptionFailures(t *testing.T) {
	page1 := fixture("consumptions_page1.json")
	html := testkit.Route{Body: "<html>maintenance</html>", Header: http.Header{"Content-Type": {"text/html"}}}
	for name, tc := range map[string]struct {
		routes map[string]testkit.Route
		check  func(error) bool
	}{
		"unauthorized": {map[string]testkit.Route{pageKey("1"): {Status: 401}}, httpx.IsAuthError},
		"forbidden":    {map[string]testkit.Route{pageKey("1"): {Status: 403}}, httpx.IsAuthError},
		"html":         {map[string]testkit.Route{pageKey("1"): html}, func(err error) bool { return errors.Is(err, httpx.ErrDecode) }},
		"no array":     {map[string]testkit.Route{pageKey("1"): {Body: `{"total_count":0}`}}, nil},
		"null array":   {map[string]testkit.Route{pageKey("1"): {Body: `{"consumptions":null,"total_count":0}`}}, nil},
		"page 2 fails": {map[string]testkit.Route{pageKey("1"): page1, pageKey("2"): {Status: 500}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := newPoller(t, tc.routes, io.Discard)
			res, err := poll(t, p)
			if err == nil {
				t.Fatalf("poll succeeded with %d metrics, want an error", len(res.Metrics))
			}
			if tc.check != nil && !tc.check(err) {
				t.Errorf("err = %v", err)
			}
			if strings.Contains(err.Error(), tOrg) {
				t.Errorf("error leaks the organization ID: %v", err)
			}
		})
	}
}

func TestConsumptionSkipsInvalidRows(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newPoller(t, map[string]testkit.Route{
		pageKey("1"): {Body: `{"consumptions":[
			{"value":{"currency_code":"EUR","units":1,"nanos":500000000},"project_id":"00000000-0000-0000-0000-000000000001","project_name":"demo-project","category_name":"Compute"},
			{"project_id":"00000000-0000-0000-0000-000000000001","project_name":"demo-project","category_name":"Compute"},
			{"value":{"units":7},"project_id":"00000000-0000-0000-0000-000000000001","project_name":"demo-project","category_name":"Compute"}
		],"total_count":3,"updated_at":"not-a-date"}`},
	}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemErrors != 3 {
		t.Errorf("ItemErrors = %d, want 3 (two rows, one timestamp)", res.ItemErrors)
	}
	compare(t, res, hdrAmount+amount("00000000-0000-0000-0000-000000000001", "demo-project", "Compute", "1.5"))
	if n := strings.Count(logs.String(), "level=WARN"); n != 3 {
		t.Errorf("warnings = %d, want 3:\n%s", n, logs.String())
	}
}
