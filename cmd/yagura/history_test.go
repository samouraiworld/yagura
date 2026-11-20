package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/samouraiworld/yagura/internal/budget"
	"github.com/samouraiworld/yagura/internal/config"
	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/scaleway"
)

func TestBuildPollersScalewayHistory(t *testing.T) {
	cfg, err := config.Load(scalewayLookup)
	if err != nil {
		t.Fatal(err)
	}
	history, err := openHistory(cfg, slog.New(slog.DiscardHandler))
	if err != nil || history == nil {
		t.Fatalf("openHistory = %v, %v; want a memory-only history", history, err)
	}
	ps, err := buildPollers(cfg, deps{logger: slog.New(slog.DiscardHandler), requests: httpx.NewRequestsCounter(), history: history})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.Name())
	}
	if want := []string{"scaleway_consumption", "scaleway_history"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestOpenHistoryOffOrInMemory(t *testing.T) {
	off := func(k string) (string, bool) {
		if k == "YAGURA_SCALEWAY_HISTORY_MONTHS" {
			return "0", true
		}
		return scalewayLookup(k)
	}
	for name, lookup := range map[string]config.LookupFunc{
		"history off":  off,
		"scaleway off": func(string) (string, bool) { return "", false },
	} {
		cfg, err := config.Load(lookup)
		if err != nil {
			t.Fatal(err)
		}
		if h, err := openHistory(cfg, slog.New(slog.DiscardHandler)); h != nil || err != nil {
			t.Errorf("%s: openHistory = %v, %v; want nil, nil", name, h, err)
		}
	}
	cfg, err := config.Load(scalewayLookup)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	if _, err := openHistory(cfg, slog.New(slog.NewTextHandler(logs, nil))); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), "memory only") != 1 {
		t.Errorf("want one memory-only warning, logs: %s", logs)
	}
}

// The monthly metric maps projects like yagura_scaleway_project_info: manual
// mapping first, else the automatic rule, which keeps a deleted project's ID
// whole.
func TestProjectMapperOnHistory(t *testing.T) {
	const (
		idAcme    = "0000000a-0000-0000-0000-000000000001"
		idSuite   = "0000000b-0000-0000-0000-000000000002"
		idDeleted = "0000000d-0000-0000-0000-000000000004"
	)
	dir := t.TempDir()
	period := time.Now().UTC().AddDate(0, 0, -time.Now().UTC().Day()+1).AddDate(0, -1, 0).Format("2006-01")
	file := `{"version":1,"months":[{"billing_period":"` + period + `","last_success":"` +
		time.Now().UTC().Format(time.RFC3339) + `","lines":[
 {"project_id":"` + idAcme + `","project_name":"acme-backups","category":"Compute","currency":"EUR","amount":1},
 {"project_id":"` + idSuite + `","project_name":"Big Suite","category":"Compute","currency":"EUR","amount":2},
 {"project_id":"` + idDeleted + `","project_name":"` + idDeleted + `","category":"Compute","currency":"EUR","amount":3}]}],
 "invoices":{"last_success":"0001-01-01T00:00:00Z","amounts":[]}}`
	if err := os.WriteFile(filepath.Join(dir, scaleway.HistoryFileName), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := scaleway.OpenHistory(scaleway.HistoryOptions{Dir: dir, Months: 24, Interval: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	store, err := budget.Open(budget.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetMapping(idSuite, "suite"); err != nil {
		t.Fatal(err)
	}
	line := func(id, name, project, v string) string {
		return `yagura_scaleway_monthly_consumption_amount{billing_period="` + period + `",category="Compute",currency="EUR",project="` +
			project + `",project_id="` + id + `",project_name="` + name + `"} ` + v + "\n"
	}
	want := "# HELP yagura_scaleway_monthly_consumption_amount Consumption per billing period (UTC calendar month), project and category, excluding VAT and before discounts, as reported by the Scaleway billing API; the current month and the last closed months.\n# TYPE yagura_scaleway_monthly_consumption_amount gauge\n" +
		line(idAcme, "acme-backups", "acme", "1") +
		line(idSuite, "Big Suite", "suite", "2") +
		line(idDeleted, idDeleted, idDeleted, "3")
	c := scaleway.NewHistoryCollector(h, projectMapper(store))
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), scaleway.HistoryMetricNames[0]); err != nil {
		t.Error(err)
	}
	// A changed mapping applies at the next scrape.
	if _, err := store.UnsetMapping(idSuite); err != nil {
		t.Fatal(err)
	}
	want = strings.Replace(want, `project="suite"`, `project="big-suite"`, 1)
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), scaleway.HistoryMetricNames[0]); err != nil {
		t.Error(err)
	}
	// Without a store, the automatic rule alone.
	if err := testutil.CollectAndCompare(scaleway.NewHistoryCollector(h, projectMapper(nil)), strings.NewReader(want), scaleway.HistoryMetricNames[0]); err != nil {
		t.Error(err)
	}
}
