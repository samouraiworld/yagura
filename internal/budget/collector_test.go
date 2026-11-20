package budget

import (
	"reflect"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/samouraiworld/yagura/internal/scaleway"
)

const tID3 = "00000000-0000-0000-0000-000000000003"

func TestResolve(t *testing.T) {
	projects := []scaleway.Project{
		{ID: tID1, Name: "acme-backups"},
		{ID: strings.ToUpper("0000000a-0000-0000-0000-000000000002"), Name: "Big Suite"},
		{ID: tID3, Name: ""},
	}
	mappings := []Mapping{
		{ProjectID: "0000000a-0000-0000-0000-000000000002", Project: "suite"},
		{ProjectID: tID2, Project: "orbit"},
	}
	want := []Resolved{
		{ProjectID: tID3, Project: tID3, Mapping: MappingAuto, InBilling: true},
		{ProjectID: tID1, ProjectName: "acme-backups", Project: "acme", Mapping: MappingAuto, InBilling: true},
		{ProjectID: tID2, Project: "orbit", Mapping: MappingManual},
		// The billing ID keeps its case: it is the join key with
		// yagura_scaleway_consumption_amount.
		{ProjectID: "0000000A-0000-0000-0000-000000000002", ProjectName: "Big Suite", Project: "suite", Mapping: MappingManual, InBilling: true},
	}
	got := Resolve(projects, mappings)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve =\n%+v\nwant\n%+v", got, want)
	}
	if !HasScalewayProject(got, "acme") || HasScalewayProject(got, "orbit") || HasScalewayProject(got, "zeta") {
		t.Error("HasScalewayProject is wrong")
	}
}

func TestCollector(t *testing.T) {
	s, _ := open(t, Options{Dir: t.TempDir(), InitialTotal: 250, InitialScaleway: 250})
	if _, _, err := s.SetBudget(ScopeProject, "acme", 120.5); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetMapping(tID3, "orbit"); err != nil {
		t.Fatal(err)
	}
	projects := []scaleway.Project{{ID: tID1, Name: "Acme"}, {ID: tID3, Name: "Tools"}}
	c := NewCollector(s, func() []scaleway.Project { return projects })
	want := `
# HELP yagura_budget_amount Monthly budget excluding VAT, per scope (total, scaleway, project) and business project, as stored by yagura.
# TYPE yagura_budget_amount gauge
yagura_budget_amount{currency="EUR",project="",scope="scaleway"} 250
yagura_budget_amount{currency="EUR",project="",scope="total"} 250
yagura_budget_amount{currency="EUR",project="acme",scope="project"} 120.5
# HELP yagura_scaleway_project_info Business project of each Scaleway project seen in the current month's billing data, always 1; mapping is auto or manual.
# TYPE yagura_scaleway_project_info gauge
yagura_scaleway_project_info{mapping="auto",project="acme",project_id="00000000-0000-0000-0000-000000000001",project_name="Acme"} 1
yagura_scaleway_project_info{mapping="manual",project="orbit",project_id="00000000-0000-0000-0000-000000000003",project_name="Tools"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), MetricNames...); err != nil {
		t.Error(err)
	}

	// A change shows at the next scrape; an unset budget disappears.
	if _, err := s.UnsetBudget(ScopeTotal, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnsetMapping(tID3); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(c, MetricNames[0]); n != 2 {
		t.Errorf("budget series = %d, want 2", n)
	}
	if err := testutil.CollectAndCompare(c, strings.NewReader(`
# HELP yagura_scaleway_project_info Business project of each Scaleway project seen in the current month's billing data, always 1; mapping is auto or manual.
# TYPE yagura_scaleway_project_info gauge
yagura_scaleway_project_info{mapping="auto",project="acme",project_id="00000000-0000-0000-0000-000000000001",project_name="Acme"} 1
yagura_scaleway_project_info{mapping="auto",project="tools",project_id="00000000-0000-0000-0000-000000000003",project_name="Tools"} 1
`), MetricNames[1]); err != nil {
		t.Error(err)
	}
}

func TestCollectorWithoutStoreOrProjects(t *testing.T) {
	c := NewCollector(nil, func() []scaleway.Project { return []scaleway.Project{{ID: tID1, Name: "Orbit"}} })
	if n := testutil.CollectAndCount(c); n != 1 {
		t.Errorf("series = %d, want 1 info series", n)
	}
	s, _ := open(t, Options{Dir: t.TempDir(), InitialTotal: 1})
	if n := testutil.CollectAndCount(NewCollector(s, nil)); n != 1 {
		t.Errorf("series = %d, want 1 budget series", n)
	}
}
