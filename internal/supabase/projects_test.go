package supabase

import (
	"bytes"
	"context"
	"log/slog"
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
	refA = "aaaaaaaaaaaaaaaaaaaa"
	refB = "bbbbbbbbbbbbbbbbbbbb"

	healthPathA = "/v1/projects/" + refA + "/health"
	healthPathB = "/v1/projects/" + refB + "/health"
	healthKeyA  = "GET " + healthPathA + "?services=auth%2Cdb%2Cpooler%2Crealtime%2Crest%2Cstorage&timeout_ms=5000"
)

const twoActiveProjects = `[
	  {"ref":"` + refA + `","name":"demo-app","organization_slug":"demo-org","region":"eu-west-1","status":"ACTIVE_HEALTHY"},
	  {"ref":"` + refB + `","name":"demo-other","organization_slug":"demo-org","region":"eu-west-1","status":"ACTIVE_HEALTHY"}
	]`

var testServices = []string{"auth", "db", "pooler", "realtime", "rest", "storage"}

func TestMain(m *testing.M) {
	httpx.SetDialGuard(httpx.LoopbackOnly)
	os.Exit(m.Run())
}

func newPoller(t *testing.T, routes map[string]testkit.Route, logs *bytes.Buffer) (poller.Poller, *httptest.Server) {
	t.Helper()
	srv := testkit.NewServer(t, routes)
	client := httpx.New(httpx.Options{Provider: "supabase", BaseURL: srv.URL})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewProjectsPoller(client, testServices, time.Minute, logger), srv
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

func TestProjectsStatusStateSet(t *testing.T) {
	p, _ := newPoller(t, map[string]testkit.Route{
		"/v1/projects": {File: "testdata/projects.json"},
		healthKeyA:     {File: "testdata/health_ok.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res)
	status := v[MetricNames[1]]
	if len(status) != 2*len(ProjectStatuses) {
		t.Fatalf("status series = %d, want %d", len(status), 2*len(ProjectStatuses))
	}
	for ref, current := range map[string]string{refA: "ACTIVE_HEALTHY", refB: "INACTIVE"} {
		ones := 0
		for _, s := range ProjectStatuses {
			got, ok := status[ref+"|"+s]
			if !ok {
				t.Fatalf("missing series %s %s", ref, s)
			}
			if got == 1 {
				ones++
				if s != current {
					t.Errorf("%s: %s = 1, want %s", ref, s, current)
				}
			}
		}
		if ones != 1 {
			t.Errorf("%s: %d series at 1, want 1", ref, ones)
		}
	}
	if got := v[MetricNames[0]]["demo-org|demo-app|"+refA+"|eu-west-1"]; got != 1 {
		t.Errorf("info series for %s = %v, want 1 (all: %v)", refA, got, v[MetricNames[0]])
	}
	if got := len(v[MetricNames[0]]); got != 2 {
		t.Errorf("info series = %d, want 2", got)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
	}
}

func TestProjectsHealth(t *testing.T) {
	p, srv := newPoller(t, map[string]testkit.Route{
		"/v1/projects": {File: "testdata/projects.json"},
		healthKeyA:     {File: "testdata/health_degraded.json"},
		healthPathB:    {Body: `[]`},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	health := values(t, res)[MetricNames[2]]
	want := map[string]float64{"auth": 1, "db": 1, "pooler": 1, "realtime": 0, "rest": 0, "storage": 1}
	if len(health) != len(want) {
		t.Errorf("health series = %d, want %d: %v", len(health), len(want), health)
	}
	for s, w := range want {
		if got, ok := health[refA+"|"+s]; !ok || got != w {
			t.Errorf("%s = %v (present %v), want %v", s, got, ok, w)
		}
	}
	if got := testkit.Hits(srv, healthKeyA); got != 1 {
		t.Errorf("health hits for A = %d, want 1", got)
	}
	if got := testkit.Hits(srv, healthPathB); got != 0 {
		t.Errorf("health hits for inactive B = %d, want 0", got)
	}
}

func TestProjectsHealthCallFails(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newPoller(t, map[string]testkit.Route{
		"/v1/projects": {Body: twoActiveProjects},
		healthPathA:    {Status: 500, Body: `{"message":"demo failure"}`},
		healthPathB:    {File: "testdata/health_ok.json"},
	}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "supabase health call failed") || !strings.Contains(out, "project_ref="+refA) {
		t.Errorf("no warning naming %s: %s", refA, out)
	}
	health := values(t, res)[MetricNames[2]]
	for _, s := range testServices {
		if got, ok := health[refA+"|"+s]; !ok || got != 0 {
			t.Errorf("failing project %s = %v (present %v), want 0", s, got, ok)
		}
		if got, ok := health[refB+"|"+s]; !ok || got != 1 {
			t.Errorf("healthy project %s = %v (present %v), want 1", s, got, ok)
		}
	}
	if res.ItemErrors != 1 {
		t.Errorf("ItemErrors = %d, want 1", res.ItemErrors)
	}
}

// TestProjectsHealthFailureLogsCause checks that the warning keeps the cause:
// with the ref and the path, the error text outgrows the cap on remote values.
func TestProjectsHealthFailureLogsCause(t *testing.T) {
	for name, tc := range map[string]struct {
		route testkit.Route
		cause string
	}{
		"server error": {testkit.Route{Status: 500, Body: `{"message":"demo failure"}`}, "HTTP 500"},
		"null body":    {testkit.Route{Body: `null`}, "response is not an array"},
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			p, _ := newPoller(t, map[string]testkit.Route{
				"/v1/projects": {Body: twoActiveProjects},
				healthPathA:    tc.route,
				healthPathB:    {File: "testdata/health_ok.json"},
			}, &logs)
			if _, err := poll(t, p); err != nil {
				t.Fatal(err)
			}
			out := logs.String()
			if !strings.Contains(out, tc.cause) || strings.Contains(out, "(truncated)") {
				t.Errorf("warning lacks the cause %q: %s", tc.cause, out)
			}
		})
	}
}

func TestProjectsHealthGone(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newPoller(t, map[string]testkit.Route{
		"/v1/projects": {Body: twoActiveProjects},
		healthPathA:    {Status: 404, Body: `{"message":"not found"}`},
		healthPathB:    {File: "testdata/health_ok.json"},
	}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	health := values(t, res)[MetricNames[2]]
	for _, s := range testServices {
		if _, ok := health[refA+"|"+s]; ok {
			t.Errorf("series emitted for the gone project: %s", s)
		}
		if got, ok := health[refB+"|"+s]; !ok || got != 1 {
			t.Errorf("%s = %v (present %v), want 1", s, got, ok)
		}
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
	}
	if out := logs.String(); !strings.Contains(out, "level=DEBUG") || !strings.Contains(out, "project gone") || !strings.Contains(out, "project_ref="+refA) {
		t.Errorf("no debug line naming %s: %s", refA, out)
	}
}

func TestProjectsHealthForbidden(t *testing.T) {
	p, _ := newPoller(t, map[string]testkit.Route{
		"/v1/projects": {File: "testdata/projects.json"},
		healthPathA:    {Status: 403, Body: `{"message":"forbidden"}`},
	}, &bytes.Buffer{})
	_, err := poll(t, p)
	if err == nil || !httpx.IsAuthError(err) {
		t.Fatalf("err = %v, want an auth error", err)
	}
}

func TestProjectsEmpty(t *testing.T) {
	p, _ := newPoller(t, map[string]testkit.Route{
		"/v1/projects": {File: "testdata/projects_empty.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Metrics) != 0 || res.ItemErrors != 0 {
		t.Errorf("metrics = %d, ItemErrors = %d, want 0 and 0", len(res.Metrics), res.ItemErrors)
	}
}

func TestProjectsUnknownStatus(t *testing.T) {
	body := `[{"ref":"` + refA + `","name":"demo-app","organization_slug":"demo-org","region":"eu-west-1","status":"FUTURE_STATE"}]`
	var logs bytes.Buffer
	p, _ := newPoller(t, map[string]testkit.Route{"/v1/projects": {Body: body}}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	status := values(t, res)[MetricNames[1]]
	if len(status) != len(ProjectStatuses)+1 {
		t.Errorf("status series = %d, want %d", len(status), len(ProjectStatuses)+1)
	}
	if got := status[refA+"|FUTURE_STATE"]; got != 1 {
		t.Errorf("FUTURE_STATE = %v, want 1", got)
	}
	for _, s := range ProjectStatuses {
		if got := status[refA+"|"+s]; got != 0 {
			t.Errorf("%s = %v, want 0", s, got)
		}
	}
	if res.ItemErrors != 1 {
		t.Errorf("ItemErrors = %d, want 1", res.ItemErrors)
	}
	if !strings.Contains(logs.String(), "unknown status") {
		t.Errorf("no warning logged: %s", logs.String())
	}
}

func TestProjectsListSkipsAndDedups(t *testing.T) {
	body := `[
	  42,
	  {"ref":"../x","name":"demo-bad","status":"ACTIVE_HEALTHY"},
	  {"ref":"` + refA + `","name":"demo-nostatus","status":""},
	  {"ref":"` + refB + `","name":"demo-first","organization_slug":"demo-org","region":"eu-west-1","status":"INACTIVE"},
	  {"ref":"` + refB + `","name":"demo-last","organization_slug":"demo-org","region":"eu-west-1","status":"INACTIVE"}
	]`
	p, _ := newPoller(t, map[string]testkit.Route{"/v1/projects": {Body: body}}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemErrors != 3 {
		t.Errorf("ItemErrors = %d, want 3", res.ItemErrors)
	}
	info := values(t, res)[MetricNames[0]]
	if len(info) != 1 || info["demo-org|demo-last|"+refB+"|eu-west-1"] != 1 {
		t.Errorf("info = %v, want only the last entry of %s", info, refB)
	}
}
