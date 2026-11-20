package scaleway

import (
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/testkit"
)

func TestCatalogFilledBySuccessfulPoll(t *testing.T) {
	srv := testkit.NewServer(t, map[string]testkit.Route{
		pageKey("1"): fixture("consumptions_page1.json"),
		pageKey("2"): fixture("consumptions_page2.json"),
	})
	client := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: srv.URL})
	catalog := NewCatalog()
	p := NewConsumptionPoller(client, tOrg, time.Hour, slog.New(slog.DiscardHandler), catalog, nil)
	if _, err := poll(t, p); err != nil {
		t.Fatal(err)
	}
	want := []Project{
		{ID: "00000000-0000-0000-0000-000000000001", Name: "demo-project"},
		{ID: "00000000-0000-0000-0000-000000000002", Name: "demo-project-2"},
	}
	if got := catalog.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}

	// A failed poll keeps the previous list, like the scheduler keeps the
	// previous metric snapshot.
	bad := testkit.NewServer(t, map[string]testkit.Route{pageKey("1"): {Body: `{"total_count":1}`}})
	badClient := httpx.New(httpx.Options{Provider: "scaleway", BaseURL: bad.URL})
	p = NewConsumptionPoller(badClient, tOrg, time.Hour, slog.New(slog.DiscardHandler), catalog, nil)
	if _, err := poll(t, p); err == nil {
		t.Fatal("poll succeeded on a page without consumptions")
	}
	if got := catalog.List(); !reflect.DeepEqual(got, want) {
		t.Errorf("List() after a failed poll = %v, want %v", got, want)
	}
}

func TestCatalogSet(t *testing.T) {
	c := NewCatalog()
	c.set([]groupKey{
		{projectID: "b", projectName: "zeta"},
		{projectID: "b", projectName: "alpha"},
		{projectID: "b", projectName: ""},
		{projectID: "", projectName: "orphan"},
		{projectID: "a", projectName: ""},
		{projectID: "c", projectName: ""},
		{projectID: "c", projectName: "named"},
		// Deleted during the month: rows with its ID as name and with its
		// real name. The real name wins, as in the monthly history.
		{projectID: "0000000d-0000-0000-0000-000000000004", projectName: "0000000d-0000-0000-0000-000000000004"},
		{projectID: "0000000d-0000-0000-0000-000000000004", projectName: "Nova"},
	})
	want := []Project{{ID: "0000000d-0000-0000-0000-000000000004", Name: "Nova"}, {ID: "a"}, {ID: "b", Name: "alpha"}, {ID: "c", Name: "named"}}
	if got := c.List(); !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %v, want %v", got, want)
	}
	got := c.List()
	got[1].Name = "mutated"
	if c.List()[1].Name != "" {
		t.Error("List returned the internal slice")
	}
	var nilCatalog *Catalog
	nilCatalog.set([]groupKey{{projectID: "a"}})
	if nilCatalog.List() != nil {
		t.Error("nil catalog listed projects")
	}
}
