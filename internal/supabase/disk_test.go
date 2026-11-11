package supabase

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
	diskPathA = "/v1/projects/" + refA + "/config/disk/util"
	diskPathB = "/v1/projects/" + refB + "/config/disk/util"

	twoActiveProjects = `[
	  {"ref":"` + refA + `","name":"demo-app","organization_slug":"demo-org","region":"eu-west-1","status":"ACTIVE_HEALTHY"},
	  {"ref":"` + refB + `","name":"demo-other","organization_slug":"demo-org","region":"eu-west-1","status":"ACTIVE_HEALTHY"}
	]`
)

func newDiskPoller(t *testing.T, routes map[string]testkit.Route, logs *bytes.Buffer) (poller.Poller, *httptest.Server) {
	t.Helper()
	srv := testkit.NewServer(t, routes)
	client := httpx.New(httpx.Options{Provider: "supabase", BaseURL: srv.URL})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewDiskPoller(client, time.Minute, logger), srv
}

func wantDisk(t *testing.T, v map[string]map[string]float64, ref string, size, used, avail float64) {
	t.Helper()
	for i, w := range []float64{size, used, avail} {
		got, ok := v[MetricNames[3+i]][ref]
		if !ok || got != w {
			t.Errorf("%s{%s} = %v (present %v), want %v", MetricNames[3+i], ref, got, ok, w)
		}
	}
}

func TestDiskUtil(t *testing.T) {
	p, _ := newDiskPoller(t, map[string]testkit.Route{
		"/v1/projects": {File: "testdata/projects.json"},
		diskPathA:      {File: "testdata/disk_util.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res)
	wantDisk(t, v, refA, 8589934592, 7516192768, 1073741824)
	for i := 3; i < 6; i++ {
		if n := len(v[MetricNames[i]]); n != 1 {
			t.Errorf("%s series = %d, want 1", MetricNames[i], n)
		}
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
	}
}

func TestDiskSkipsInactive(t *testing.T) {
	p, srv := newDiskPoller(t, map[string]testkit.Route{
		"/v1/projects": {File: "testdata/projects.json"},
		diskPathA:      {File: "testdata/disk_util.json"},
		diskPathB:      {File: "testdata/disk_util.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := testkit.Hits(srv, diskPathB); got != 0 {
		t.Errorf("disk hits for inactive B = %d, want 0", got)
	}
	if got := testkit.Hits(srv, diskPathA); got != 1 {
		t.Errorf("disk hits for A = %d, want 1", got)
	}
	if n := len(values(t, res)[MetricNames[3]]); n != 1 {
		t.Errorf("size series = %d, want 1", n)
	}
}

func TestDiskItemFailure(t *testing.T) {
	p, _ := newDiskPoller(t, map[string]testkit.Route{
		"/v1/projects": {Body: twoActiveProjects},
		diskPathA:      {Status: 500, Body: `{"message":"demo failure"}`},
		diskPathB:      {File: "testdata/disk_util.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res)
	for i := 3; i < 6; i++ {
		if _, ok := v[MetricNames[i]][refA]; ok {
			t.Errorf("%s has a series for the failing project", MetricNames[i])
		}
	}
	wantDisk(t, v, refB, 8589934592, 7516192768, 1073741824)
	if res.ItemErrors != 1 {
		t.Errorf("ItemErrors = %d, want 1", res.ItemErrors)
	}
}

func TestDiskProjectGone(t *testing.T) {
	p, _ := newDiskPoller(t, map[string]testkit.Route{
		"/v1/projects": {Body: twoActiveProjects},
		diskPathA:      {Status: 404, Body: `{"message":"not found"}`},
		diskPathB:      {File: "testdata/disk_util.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res)
	if _, ok := v[MetricNames[3]][refA]; ok {
		t.Error("series emitted for the missing project")
	}
	wantDisk(t, v, refB, 8589934592, 7516192768, 1073741824)
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
	}
}

func TestDiskMissingField(t *testing.T) {
	var logs bytes.Buffer
	p, _ := newDiskPoller(t, map[string]testkit.Route{
		"/v1/projects": {File: "testdata/projects.json"},
		diskPathA:      {Body: `{"timestamp":"2031-04-05T06:07:08Z","metrics":{"fs_size_bytes":100,"fs_used_bytes":40}}`},
	}, &logs)
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res)
	if got := v[MetricNames[3]][refA]; got != 100 {
		t.Errorf("size = %v, want 100", got)
	}
	if got := v[MetricNames[4]][refA]; got != 40 {
		t.Errorf("used = %v, want 40", got)
	}
	if _, ok := v[MetricNames[5]][refA]; ok {
		t.Error("available series emitted although fs_avail_bytes is missing")
	}
	if res.ItemErrors != 1 {
		t.Errorf("ItemErrors = %d, want 1", res.ItemErrors)
	}
	if !bytes.Contains(logs.Bytes(), []byte("fs_avail_bytes")) {
		t.Errorf("no warning naming the missing field: %s", logs.String())
	}
}
