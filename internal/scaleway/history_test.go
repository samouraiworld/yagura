package scaleway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/testkit"
)

const (
	tProjectA = "0000000a-0000-0000-0000-000000000001" // renamed: Orbit-legacy, then Tools
	tProjectB = "0000000b-0000-0000-0000-000000000002" // a zero line
	tProjectD = "0000000d-0000-0000-0000-000000000004" // deleted: Nova, then its ID
	tProjectE = "0000000e-0000-0000-0000-000000000005" // deleted, never named
)

var tNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func histKey(period, page string) string {
	return tPath + "?billing_period=" + period + "&organization_id=" + tOrg + "&page=" + page + "&page_size=100"
}

func invKey(page string) string {
	return invoicesPath + "?order_by=invoice_number_asc&organization_id=" + tOrg + "&page=" + page + "&page_size=100"
}

func historyRoutes() map[string]testkit.Route {
	return map[string]testkit.Route{
		histKey("2026-10", "1"): fixture("consumptions_empty.json"),
		histKey("2026-09", "1"): fixture("history_2026-09_page1.json"),
		histKey("2026-09", "2"): fixture("history_2026-09_page2.json"),
		histKey("2026-08", "1"): fixture("history_2026-08.json"),
		invKey("1"):             fixture("invoices_page1.json"),
		invKey("2"):             fixture("invoices_page2.json"),
	}
}

func openHistory(t *testing.T, dir string, clock *fakeClock, months int) (*History, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	h, err := OpenHistory(HistoryOptions{Dir: dir, Months: months, Interval: 24 * time.Hour, Now: clock.Now,
		Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return h, logs
}

func newHistoryPoller(t *testing.T, srvURL string, h *History, logs io.Writer) poller.Poller {
	t.Helper()
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srvURL})
	return NewHistoryPoller(client, tOrg, h, slog.New(slog.NewTextHandler(logs, nil)))
}

// testMapper maps a named project to "biz-<name>" and leaves a project named
// by its ID out, which then gets its ID as business project.
func testMapper(projects []Project) map[string]string {
	out := map[string]string{}
	for _, p := range projects {
		if p.Name != p.ID {
			out[p.ID] = "biz-" + strings.ToLower(p.Name)
		}
	}
	return out
}

const (
	hdrMonthly = "# HELP yagura_scaleway_monthly_consumption_amount Consumption per billing period (UTC calendar month), project and category, excluding VAT and before discounts, as reported by the Scaleway billing API; the current month and the last closed months.\n# TYPE yagura_scaleway_monthly_consumption_amount gauge\n"
	hdrInvoice = "# HELP yagura_scaleway_invoice_amount Sum over a closed billing period's Scaleway invoices of total_untaxed (kind=untaxed, after discount) or total_discount (kind=discount, negative).\n# TYPE yagura_scaleway_invoice_amount gauge\n"
	hdrSuccess = "# HELP yagura_scaleway_monthly_last_success_timestamp_seconds Unix time of the last successful read of a billing period's consumption.\n# TYPE yagura_scaleway_monthly_last_success_timestamp_seconds gauge\n"
)

func monthly(period, id, name, project, category, v string) string {
	return `yagura_scaleway_monthly_consumption_amount{billing_period="` + period + `",category="` + category +
		`",currency="EUR",project="` + project + `",project_id="` + id + `",project_name="` + name + `"} ` + v + "\n"
}

// wantHistory is the exposition after reading the fixtures at tNow with a
// three-month window. Project A keeps its latest name on both months; the
// deleted project D keeps the name it had before deletion; E, never named,
// is named and mapped by its ID. 2026-10 is empty: a success series only.
var wantHistory = hdrMonthly +
	monthly("2026-08", tProjectA, "Tools", "biz-tools", "Compute", "3") +
	monthly("2026-08", tProjectD, "Nova", "biz-nova", "Object Storage", "1") +
	monthly("2026-09", tProjectA, "Tools", "biz-tools", "Compute", "1.75") +
	monthly("2026-09", tProjectB, "Acme", "biz-acme", "Containers", "0") +
	monthly("2026-09", tProjectD, "Nova", "biz-nova", "Object Storage", "2") +
	monthly("2026-09", tProjectE, tProjectE, tProjectE, "Serverless", "0.01") +
	hdrInvoice +
	`yagura_scaleway_invoice_amount{billing_period="2026-08",currency="EUR",kind="discount"} 0` + "\n" +
	`yagura_scaleway_invoice_amount{billing_period="2026-08",currency="EUR",kind="untaxed"} 100` + "\n" +
	`yagura_scaleway_invoice_amount{billing_period="2026-09",currency="EUR",kind="discount"} -4.15` + "\n" +
	`yagura_scaleway_invoice_amount{billing_period="2026-09",currency="EUR",kind="untaxed"} 427.83` + "\n" +
	hdrSuccess +
	`yagura_scaleway_monthly_last_success_timestamp_seconds{billing_period="2026-08"} 1795176000` + "\n" +
	`yagura_scaleway_monthly_last_success_timestamp_seconds{billing_period="2026-09"} 1795176000` + "\n" +
	`yagura_scaleway_monthly_last_success_timestamp_seconds{billing_period="2026-10"} 1795176000` + "\n"

func compareHistory(t *testing.T, h *History, want string) {
	t.Helper()
	if err := testutil.CollectAndCompare(NewHistoryCollector(h, testMapper), strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

func TestHistoryPollReadsMonthsAndInvoices(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, dir, clock, 3)
	srv := testkit.NewServer(t, historyRoutes())
	p := newHistoryPoller(t, srv.URL, h, io.Discard)
	if p.Name() != "scaleway_history" || p.Interval() != 15*time.Minute {
		t.Errorf("Name/Interval = %q/%v", p.Name(), p.Interval())
	}
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Metrics) != 0 || res.ItemErrors != 0 {
		t.Errorf("result = %+v; the collector exposes the history, not the poller", res)
	}
	compareHistory(t, h, wantHistory)
	for key := range historyRoutes() {
		if n := testkit.Hits(srv, "GET "+key); n != 1 {
			t.Errorf("%s requested %d times, want 1", key, n)
		}
	}
	info, err := os.Stat(filepath.Join(dir, HistoryFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("history file mode = %v, want 0600", info.Mode().Perm())
	}
	got := map[string][]string{}
	for _, p := range h.Projects() {
		got[p.ID] = append(got[p.ID], p.Name)
	}
	want := map[string][]string{tProjectA: {"Tools"}, tProjectB: {"Acme"}, tProjectD: {"Nova"}, tProjectE: {tProjectE}}
	if len(got) != len(want) {
		t.Errorf("Projects = %v", got)
	}
	for id, names := range want {
		if strings.Join(got[id], ",") != strings.Join(names, ",") {
			t.Errorf("Projects[%s] = %v, want %v", id, got[id], names)
		}
	}
}

// At startup the stored months show at once, and only the months whose last
// success is older than the interval are read again.
func TestHistoryRestartReadsOnlyDueMonths(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, dir, clock, 3)
	srv := testkit.NewServer(t, historyRoutes())
	if _, err := poll(t, newHistoryPoller(t, srv.URL, h, io.Discard)); err != nil {
		t.Fatal(err)
	}

	clock.Set(tNow.Add(time.Hour))
	h2, _ := openHistory(t, dir, clock, 3)
	compareHistory(t, h2, wantHistory)
	idle := testkit.NewServer(t, nil) // any request fails the test
	if _, err := poll(t, newHistoryPoller(t, idle.URL, h2, io.Discard)); err != nil {
		t.Fatal(err)
	}

	clock.Set(tNow.Add(24 * time.Hour))
	if _, err := poll(t, newHistoryPoller(t, srv.URL, h2, io.Discard)); err != nil {
		t.Fatal(err)
	}
	if n := testkit.Hits(srv, "GET "+histKey("2026-09", "1")); n != 2 {
		t.Errorf("2026-09 read %d times after the interval, want 2", n)
	}
	if n := testkit.Hits(srv, "GET "+invKey("1")); n != 2 {
		t.Errorf("invoices read %d times after the interval, want 2", n)
	}
}

func failingRoutes(failing ...string) map[string]testkit.Route {
	routes := historyRoutes()
	for _, k := range failing {
		routes[k] = testkit.Route{Status: http.StatusBadRequest, Body: `{"message":"boom"}`}
	}
	return routes
}

// A failed read keeps the stored values, logs one error line and is
// retried an hour later; the poll fails until every read has succeeded.
func TestHistoryFailureKeepsStoredValues(t *testing.T) {
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, t.TempDir(), clock, 3)
	srv := testkit.NewServer(t, historyRoutes())
	if _, err := poll(t, newHistoryPoller(t, srv.URL, h, io.Discard)); err != nil {
		t.Fatal(err)
	}

	clock.Set(tNow.Add(25 * time.Hour))
	bad := testkit.NewServer(t, failingRoutes(histKey("2026-09", "1"), invKey("1")))
	logs := &bytes.Buffer{}
	_, err := poll(t, newHistoryPoller(t, bad.URL, h, logs))
	if err == nil || !strings.Contains(err.Error(), "2026-09") || !strings.Contains(err.Error(), "invoices") {
		t.Fatalf("poll error = %v, want the failing month and invoices named", err)
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 2 {
		t.Errorf("error lines = %d, want 2 (one per failed read):\n%s", n, logs)
	}
	if !strings.Contains(logs.String(), "billing_period=2026-09") {
		t.Errorf("the error line does not name the month:\n%s", logs)
	}
	// Stored values stay, never 0; the other months were read again.
	want := strings.ReplaceAll(wantHistory, `{billing_period="2026-08"} 1795176000`, `{billing_period="2026-08"} 1795266000`)
	want = strings.ReplaceAll(want, `{billing_period="2026-10"} 1795176000`, `{billing_period="2026-10"} 1795266000`)
	compareHistory(t, h, want)

	// 30 minutes later: no new attempt, still failing.
	clock.Set(tNow.Add(25*time.Hour + 30*time.Minute))
	if _, err := poll(t, newHistoryPoller(t, bad.URL, h, io.Discard)); err == nil {
		t.Error("poll succeeded while reads are still failing")
	}
	if n := testkit.Hits(bad, "GET "+histKey("2026-09", "1")); n != 1 {
		t.Errorf("2026-09 attempts after 30 minutes = %d, want 1 (no retry before an hour)", n)
	}
	if n := testkit.Hits(bad, "GET "+invKey("1")); n != 1 {
		t.Errorf("invoice attempts after 30 minutes = %d, want 1", n)
	}

	// An hour after the failure, the retry happens; it fails again here.
	clock.Set(tNow.Add(26*time.Hour + time.Minute))
	if _, err := poll(t, newHistoryPoller(t, bad.URL, h, io.Discard)); err == nil {
		t.Error("poll succeeded while reads are still failing")
	}
	if n := testkit.Hits(bad, "GET "+histKey("2026-09", "1")); n != 2 {
		t.Errorf("2026-09 attempts after an hour = %d, want 2", n)
	}

	// The next retry succeeds, and the poll too.
	clock.Set(tNow.Add(27*time.Hour + 2*time.Minute))
	if _, err := poll(t, newHistoryPoller(t, srv.URL, h, io.Discard)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := h.failing(); len(got) != 0 {
		t.Errorf("failing after the retry = %v", got)
	}
}

// With nothing stored, a failed month has no series until it is read.
func TestHistoryFailureWithoutStoredValues(t *testing.T) {
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, "", clock, 3)
	bad := testkit.NewServer(t, failingRoutes(histKey("2026-09", "1")))
	if _, err := poll(t, newHistoryPoller(t, bad.URL, h, io.Discard)); err == nil {
		t.Fatal("want an error")
	}
	n := testutil.CollectAndCount(NewHistoryCollector(h, testMapper), HistoryMetricNames[2])
	if n != 2 {
		t.Errorf("success series = %d, want 2 (2026-08 and 2026-10)", n)
	}
	text := collectText(t, h)
	for _, name := range []string{HistoryMetricNames[0], HistoryMetricNames[2]} {
		if strings.Contains(text, name+` billing_period="2026-09"`) {
			t.Errorf("a month never read has a %s series", name)
		}
	}
}

// collectText gathers the history collector through a pedantic registry and
// returns one line per series: name and labels.
func collectText(t *testing.T, h *History) string {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(NewHistoryCollector(h, testMapper))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			b.WriteString(mf.GetName())
			for _, lp := range m.GetLabel() {
				b.WriteString(" " + lp.GetName() + "=" + strconv.Quote(lp.GetValue()))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// The current month comes from the consumption poll; at the month change
// its series keep their labels as a closed month, which is due at once.
func TestHistoryCurrentMonthAndMonthChange(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 30, 23, 30, 0, 0, time.UTC)}
	h, _ := openHistory(t, t.TempDir(), clock, 3)
	srv := testkit.NewServer(t, map[string]testkit.Route{
		pageKey("1"): fixture("consumptions_page1.json"),
		pageKey("2"): fixture("consumptions_page2.json"),
	})
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL})
	cp := NewConsumptionPoller(client, tOrg, time.Hour, slog.New(slog.DiscardHandler), nil, h)
	if _, err := poll(t, cp); err != nil {
		t.Fatal(err)
	}
	current := hdrMonthly +
		monthly("2026-11", "00000000-0000-0000-0000-000000000001", "demo-project", "biz-demo", "Compute", "4") +
		monthly("2026-11", "00000000-0000-0000-0000-000000000001", "demo-project", "biz-demo", "Storage", "0.1") +
		monthly("2026-11", "00000000-0000-0000-0000-000000000002", "demo-project-2", "biz-demo-project-2", "Compute", "1")
	mapper := func(ps []Project) map[string]string {
		out := testMapper(ps)
		for id, p := range out {
			out[id] = strings.TrimSuffix(p, "-project")
		}
		return out
	}
	check := func(when string) {
		t.Helper()
		if err := testutil.CollectAndCompare(NewHistoryCollector(h, mapper), strings.NewReader(current), HistoryMetricNames[0]); err != nil {
			t.Errorf("%s: %v", when, err)
		}
	}
	check("current month")
	if months, _ := h.due(clock.Now()); strings.Contains(strings.Join(months, ","), "2026-11") {
		t.Errorf("due before the month change = %v", months)
	}

	clock.Set(time.Date(2026, 12, 1, 0, 5, 0, 0, time.UTC))
	check("after the month change")
	months, _ := h.due(clock.Now())
	if len(months) == 0 || months[0] != "2026-11" {
		t.Errorf("due after the month change = %v, want 2026-11 first", months)
	}

	// A poll that crosses the month change does not update the current month.
	h2, _ := openHistory(t, "", clock, 3)
	start := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	h2.observeCurrent(start, start.Add(2*time.Second), map[groupKey]total{{"p", "n", "c", "EUR"}: {units: 1}}, "")
	if len(h2.view(start.Add(2*time.Second)).months) != 0 {
		t.Error("a poll across the month change updated the history")
	}
}

func writeHistoryFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, HistoryFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenHistoryErrors(t *testing.T) {
	ok := `{"version":1,"months":[{"billing_period":"2026-10","last_success":"2026-11-01T00:00:00Z","lines":[]}],"invoices":{"last_success":"0001-01-01T00:00:00Z","amounts":[]}}`
	line := `{"project_id":"p","project_name":"n","category":"c","currency":"EUR","amount":1}`
	cases := map[string]string{
		"corrupt":         `{"version":1,`,
		"unknown field":   strings.Replace(ok, `"version":1`, `"version":1,"extra":2`, 1),
		"version":         strings.Replace(ok, `"version":1`, `"version":2`, 1),
		"trailing data":   ok + ` {}`,
		"bad period":      strings.Replace(ok, `"2026-10"`, `"2026-13"`, 1),
		"no last_success": strings.Replace(ok, `"last_success":"2026-11-01T00:00:00Z"`, `"last_success":"0001-01-01T00:00:00Z"`, 1),
		"duplicate month": strings.Replace(ok, `"months":[`, `"months":[{"billing_period":"2026-10","last_success":"2026-11-01T00:00:00Z","lines":[]},`, 1),
		"duplicate line":  strings.Replace(ok, `"lines":[]`, `"lines":[`+line+`,`+line+`]`, 1),
		"no currency":     strings.Replace(ok, `"lines":[]`, `"lines":[`+strings.Replace(line, `"EUR"`, `""`, 1)+`]`, 1),
		"bad kind":        strings.Replace(ok, `"amounts":[]`, `"amounts":[{"billing_period":"2026-10","kind":"tax","currency":"EUR","amount":1}]`, 1),
		"amounts without last_success": strings.Replace(ok, `"amounts":[]`,
			`"amounts":[{"billing_period":"2026-10","kind":"untaxed","currency":"EUR","amount":1}]`, 1),
	}
	clock := &fakeClock{t: tNow}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeHistoryFile(t, dir, content)
			if _, err := OpenHistory(HistoryOptions{Dir: dir, Months: 3, Interval: time.Hour, Now: clock.Now}); err == nil {
				t.Fatal("want an error, never a silent reset")
			}
			if got, _ := os.ReadFile(filepath.Join(dir, HistoryFileName)); string(got) != content { //nolint:gosec // G304: a test directory
				t.Error("the file was changed")
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		writeHistoryFile(t, dir, ok)
		if _, err := OpenHistory(HistoryOptions{Dir: dir, Months: 3, Interval: time.Hour, Now: clock.Now}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		if err := os.WriteFile(target, []byte(ok), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, HistoryFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenHistory(HistoryOptions{Dir: dir, Months: 3, Interval: time.Hour}); err == nil ||
			!strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("symlink: %v", err)
		}
	})
	t.Run("not a directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenHistory(HistoryOptions{Dir: file, Months: 3, Interval: time.Hour}); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("read-only directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dir := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // G302: restore the directory
		writeHistoryFile(t, dir, ok)
		if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: a read-only directory, on purpose
			t.Fatal(err)
		}
		logs := &bytes.Buffer{}
		h, err := OpenHistory(HistoryOptions{Dir: dir, Months: 3, Interval: time.Hour, Now: clock.Now,
			Logger: slog.New(slog.NewTextHandler(logs, nil))})
		if err != nil {
			t.Fatalf("a read-only directory must not stop the start: %v", err)
		}
		if !strings.Contains(logs.String(), "not writable") {
			t.Errorf("no warning: %s", logs)
		}
		if !strings.Contains(collectText(t, h), `billing_period="2026-10"`) {
			t.Error("the stored months are not exposed")
		}
		h.setMonth("2026-09", nil, tNow)
		if err := h.save(); err != nil {
			t.Errorf("save in memory-only mode: %v", err)
		}
	})
	t.Run("last_success out of range", func(t *testing.T) {
		dir := t.TempDir()
		writeHistoryFile(t, dir, strings.Replace(ok, `"2026-11-01T00:00:00Z"`, `"3000-01-01T00:00:00Z"`, 1))
		if _, err := OpenHistory(HistoryOptions{Dir: dir, Months: 3, Interval: time.Hour, Now: clock.Now}); err == nil {
			t.Fatal("want an error")
		}
	})
}

// Months outside the window are dropped at open and from the next save.
func TestHistoryPrunesOutsideWindow(t *testing.T) {
	dir := t.TempDir()
	writeHistoryFile(t, dir, `{"version":1,"months":[
 {"billing_period":"2026-05","last_success":"2026-06-02T00:00:00Z","lines":[{"project_id":"p","project_name":"n","category":"c","currency":"EUR","amount":1}]},
 {"billing_period":"2026-10","last_success":"2026-11-02T00:00:00Z","lines":[]}],
 "invoices":{"last_success":"2026-11-02T00:00:00Z","amounts":[
 {"billing_period":"2026-05","kind":"untaxed","currency":"EUR","amount":1},
 {"billing_period":"2026-10","kind":"untaxed","currency":"EUR","amount":2}]}}`)
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, dir, clock, 3)
	text := collectText(t, h)
	if strings.Contains(text, "2026-05") || !strings.Contains(text, "2026-10") {
		t.Errorf("exposition:\n%s", text)
	}
	h.setMonth("2026-09", nil, tNow)
	if err := h.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, HistoryFileName)) //nolint:gosec // G304: a test directory
	if err != nil {
		t.Fatal(err)
	}
	var f historyFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Months) != 2 || f.Months[0].BillingPeriod != "2026-09" || len(f.Invoices.Amounts) != 1 {
		t.Errorf("saved file = %+v", f)
	}
}

func TestReadInvoicesErrors(t *testing.T) {
	cases := map[string]string{
		"no invoices array": `{"total_count":0}`,
		"no billing period": `{"invoices":[{"id":"i","total_untaxed":{"currency_code":"EUR","units":1}}],"total_count":1}`,
		"bad billing period": `{"invoices":[{"id":"i","billing_period":"last month",` +
			`"total_untaxed":{"currency_code":"EUR","units":1}}],"total_count":1}`,
		"no total_untaxed": `{"invoices":[{"id":"i","billing_period":"2026-09-01T00:00:00Z"}],"total_count":1}`,
		"discount without currency": `{"invoices":[{"id":"i","billing_period":"2026-09-01T00:00:00Z",` +
			`"total_untaxed":{"currency_code":"EUR","units":1},"total_discount":{"units":-1}}],"total_count":1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := testkit.NewServer(t, map[string]testkit.Route{invKey("1"): {Body: body}})
			client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL})
			if _, err := readInvoices(context.Background(), client, tOrg, nil); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	// An empty list is a success with no amounts.
	srv := testkit.NewServer(t, map[string]testkit.Route{invKey("1"): {Body: `{"invoices":[],"total_count":0}`}})
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL})
	if got, err := readInvoices(context.Background(), client, tOrg, nil); err != nil || len(got) != 0 {
		t.Errorf("empty list = %v, %v", got, err)
	}
	if p, err := invoicePeriod("2026-09"); err != nil || p != "2026-09" {
		t.Errorf("invoicePeriod(YYYY-MM) = %q, %v", p, err)
	}
}

func TestHistoryShutdownIsNotAFailure(t *testing.T) {
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, "", clock, 3)
	srv := testkit.NewServer(t, historyRoutes())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newHistoryPoller(t, srv.URL, h, io.Discard).Poll(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Poll on a canceled context = %v", err)
	}
	if got := h.failing(); len(got) != 0 {
		t.Errorf("failing after shutdown = %v", got)
	}
}

// An empty page before total_count is an incomplete answer: the stored
// month is kept and the read fails.
func TestHistoryEmptyPageBeforeTotalCount(t *testing.T) {
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, "", clock, 3)
	srv := testkit.NewServer(t, historyRoutes())
	if _, err := poll(t, newHistoryPoller(t, srv.URL, h, io.Discard)); err != nil {
		t.Fatal(err)
	}
	routes := historyRoutes()
	routes[histKey("2026-09", "2")] = testkit.Route{Body: `{"consumptions":[],"total_count":5}`}
	routes[invKey("2")] = testkit.Route{Body: `{"invoices":[],"total_count":4}`}
	short := testkit.NewServer(t, routes)
	clock.Set(tNow.Add(25 * time.Hour))
	_, err := poll(t, newHistoryPoller(t, short.URL, h, io.Discard))
	if err == nil || !strings.Contains(err.Error(), "2026-09") || !strings.Contains(err.Error(), "invoices") {
		t.Fatalf("poll = %v, want 2026-09 and invoices failing", err)
	}
	text := collectText(t, h)
	if !strings.Contains(text, HistoryMetricNames[0]+` billing_period="2026-09"`) || !strings.Contains(text, HistoryMetricNames[1]+` billing_period="2026-09"`) {
		t.Errorf("stored 2026-09 values lost:\n%s", text)
	}
}

func TestReadInvoicesRequiresID(t *testing.T) {
	srv := testkit.NewServer(t, map[string]testkit.Route{invKey("1"): {Body: `{"invoices":[{"billing_period":"2026-09-01T00:00:00Z",` +
		`"total_untaxed":{"currency_code":"EUR","units":1}}],"total_count":1}`}})
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL})
	if _, err := readInvoices(context.Background(), client, tOrg, nil); err == nil || !strings.Contains(err.Error(), "no id") {
		t.Fatalf("readInvoices = %v, want an error for an invoice without id", err)
	}
}

// The poll deadline stops the reads without marking a failure: the
// remaining months stay due for the next check.
func TestHistoryPollDeadlineIsNotAFailure(t *testing.T) {
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, "", clock, 3)
	srv := testkit.NewServer(t, historyRoutes())
	// One request at once, the next one a second later: past the deadline.
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL, RateLimit: 1, Burst: 1})
	p := NewHistoryPoller(client, tOrg, h, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := p.Poll(ctx)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Poll = %v, want a deadline error", err)
	}
	if got := h.failing(); len(got) != 0 {
		t.Errorf("failing = %v, want none", got)
	}
	months, invoices := h.due(clock.Now())
	if strings.Join(months, ",") != "2026-09,2026-08" || !invoices {
		t.Errorf("due = %v, %v; want 2026-09,2026-08 and the invoices", months, invoices)
	}
}

// A last success in the future (clock change) does not postpone the read.
func TestHistoryFutureLastSuccessIsDue(t *testing.T) {
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, "", clock, 1)
	h.setMonth("2026-10", nil, tNow.Add(48*time.Hour))
	h.setInvoices(nil, tNow.Add(48*time.Hour))
	months, invoices := h.due(tNow)
	if strings.Join(months, ",") != "2026-10" || !invoices {
		t.Errorf("due = %v, %v", months, invoices)
	}
}

// An unchanged current month does not rewrite the file; a save over the
// size limit is refused and keeps the previous file.
func TestHistorySaves(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, dir, clock, 3)
	totals := map[groupKey]total{{"0000000a-0000-0000-0000-000000000001", "Tools", "Compute", "EUR"}: {units: 1}}
	h.observeCurrent(tNow, tNow, totals, "")
	path := filepath.Join(dir, HistoryFileName)
	first, err := os.ReadFile(path) //nolint:gosec // G304: a test directory
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(tNow.Add(time.Hour))
	h.observeCurrent(tNow.Add(time.Hour), tNow.Add(time.Hour), totals, "")
	if again, _ := os.ReadFile(path); string(again) != string(first) { //nolint:gosec // G304: a test directory
		t.Error("an unchanged current month rewrote the file")
	}
	if !strings.Contains(collectText(t, h), `billing_period="2026-11"`) {
		t.Error("current month missing")
	}

	old := historyMaxBytes
	historyMaxBytes = len(first)
	t.Cleanup(func() { historyMaxBytes = old })
	h.setMonth("2026-10", []Line{{ProjectID: "p", ProjectName: "n", Category: "c", Currency: "EUR", Amount: 1}}, tNow)
	if err := h.save(); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("save over the limit = %v", err)
	}
	if again, _ := os.ReadFile(path); string(again) != string(first) { //nolint:gosec // G304: a test directory
		t.Error("a refused save changed the file")
	}
}

func TestHistoryProjectsSkipEmptyID(t *testing.T) {
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, "", clock, 3)
	h.setMonth("2026-10", []Line{
		{ProjectID: "", ProjectName: "", Category: "c", Currency: "EUR", Amount: 1},
		{ProjectID: "p", ProjectName: "n", Category: "c", Currency: "EUR", Amount: 1},
	}, tNow)
	if got := h.Projects(); len(got) != 1 || got[0].ID != "p" {
		t.Errorf("Projects = %v", got)
	}
}

// A refused key stops the reads after the first failure.
func TestHistoryAuthErrorStopsReads(t *testing.T) {
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, "", clock, 3)
	routes := historyRoutes()
	routes[histKey("2026-10", "1")] = testkit.Route{Status: http.StatusForbidden, Body: `{"message":"denied"}`}
	srv := testkit.NewServer(t, routes)
	if _, err := poll(t, newHistoryPoller(t, srv.URL, h, io.Discard)); err == nil {
		t.Fatal("want an error")
	}
	for _, key := range []string{histKey("2026-09", "1"), histKey("2026-08", "1"), invKey("1")} {
		if n := testkit.Hits(srv, "GET "+key); n != 0 {
			t.Errorf("%s requested %d times after a refused key", key, n)
		}
	}
}

// A duplicate invoice from a shifted page does not end the listing early.
func TestReadInvoicesCountsDistinctInvoices(t *testing.T) {
	inv := func(id string) string {
		return `{"id":"` + id + `","billing_period":"2026-09-01T00:00:00Z","total_untaxed":{"currency_code":"EUR","units":1}}`
	}
	srv := testkit.NewServer(t, map[string]testkit.Route{
		invKey("1"): {Body: `{"invoices":[` + inv("a") + `,` + inv("b") + `],"total_count":3}`},
		invKey("2"): {Body: `{"invoices":[` + inv("b") + `],"total_count":3}`},
		invKey("3"): {Body: `{"invoices":[` + inv("c") + `],"total_count":3}`},
	})
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL})
	got, err := readInvoices(context.Background(), client, tOrg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if a.Kind == KindUntaxed && a.Amount != 3 {
			t.Errorf("untaxed = %v, want 3 (a, b, c once each)", a.Amount)
		}
	}
}

// Just after the month change, an answer last updated before the month began
// is the previous month's data: it does not become the new month.
func TestHistoryCurrentMonthIgnoresStaleAnswer(t *testing.T) {
	clock := &fakeClock{t: tNow}
	h, _ := openHistory(t, "", clock, 3)
	at := time.Date(2026, 12, 1, 0, 10, 0, 0, time.UTC)
	totals := map[groupKey]total{{"p", "n", "c", "EUR"}: {units: 400}}
	h.observeCurrent(at, at, totals, "2026-11-30T23:50:00Z")
	if len(h.view(at).months) != 0 {
		t.Error("previous month's data stored as the new month")
	}
	h.observeCurrent(at, at, totals, "2026-12-01T00:05:00Z")
	if len(h.view(at).months) != 1 {
		t.Error("an answer updated this month was not stored")
	}
}

// A closed month is read strictly: an invalid row or data that changes
// between pages fails the read and keeps the stored values.
func TestHistoryStrictClosedMonthRead(t *testing.T) {
	cases := map[string]string{
		"row without value": `{"consumptions":[{"project_id":"p","project_name":"n","category_name":"c"}],"total_count":5,"updated_at":"2026-10-01T06:00:00Z"}`,
		"updated_at changed": `{"consumptions":[{"value":{"currency_code":"EUR","units":1},"project_id":"p","project_name":"n","category_name":"c"}],` +
			`"total_count":5,"updated_at":"2026-10-03T06:00:00Z"}`,
	}
	for name, page2 := range cases {
		t.Run(name, func(t *testing.T) {
			clock := &fakeClock{t: tNow}
			h, _ := openHistory(t, "", clock, 3)
			srv := testkit.NewServer(t, historyRoutes())
			if _, err := poll(t, newHistoryPoller(t, srv.URL, h, io.Discard)); err != nil {
				t.Fatal(err)
			}
			routes := historyRoutes()
			routes[histKey("2026-09", "2")] = testkit.Route{Body: page2}
			bad := testkit.NewServer(t, routes)
			clock.Set(tNow.Add(25 * time.Hour))
			if _, err := poll(t, newHistoryPoller(t, bad.URL, h, io.Discard)); err == nil || !strings.Contains(err.Error(), "2026-09") {
				t.Fatalf("poll = %v, want 2026-09 failing", err)
			}
			if !strings.Contains(collectText(t, h), HistoryMetricNames[0]+` billing_period="2026-09"`) {
				t.Error("stored 2026-09 values lost")
			}
		})
	}
}

// An invoice outside the kept periods is not checked: an old malformed
// invoice does not block the recent totals.
func TestReadInvoicesSkipsPeriodsNotKept(t *testing.T) {
	srv := testkit.NewServer(t, map[string]testkit.Route{invKey("1"): {Body: `{"invoices":[` +
		`{"id":"old","billing_period":"2020-01-01T00:00:00Z"},` +
		`{"id":"new","billing_period":"2026-09-01T00:00:00Z","total_untaxed":{"currency_code":"EUR","units":5}}],"total_count":2}`}})
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL})
	got, err := readInvoices(context.Background(), client, tOrg, func(p string) bool { return p >= "2026-01" })
	if err != nil || len(got) != 2 || got[1].Kind != KindUntaxed || got[1].Amount != 5 {
		t.Errorf("readInvoices = %+v, %v", got, err)
	}
}
