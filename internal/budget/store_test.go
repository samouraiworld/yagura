package budget

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var tNow = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

const (
	tID1 = "00000000-0000-0000-0000-000000000001"
	tID2 = "00000000-0000-0000-0000-000000000002"
)

func open(t *testing.T, opts Options) (*Store, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if opts.Now == nil {
		opts.Now = func() time.Time { return tNow }
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s, &logs
}

func readFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, FileName)) //nolint:gosec // G304: a test directory
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOpenSeedsOnlyWhenFileIsMissing(t *testing.T) {
	dir := t.TempDir()
	s, logs := open(t, Options{Dir: dir, InitialTotal: 250, InitialScaleway: 250.5})
	want := []Budget{
		{Scope: ScopeScaleway, Amount: 250.5, Currency: Currency, UpdatedAt: tNow},
		{Scope: ScopeTotal, Amount: 250, Currency: Currency, UpdatedAt: tNow},
	}
	if got := s.Budgets(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Budgets() = %v, want %v", got, want)
	}
	if !strings.Contains(logs.String(), "budget store created") {
		t.Errorf("logs = %q", logs.String())
	}
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if _, _, err := s.SetBudget(ScopeTotal, "", 300); err != nil {
		t.Fatal(err)
	}

	// Second start: the file exists, the seeds are ignored.
	s2, logs2 := open(t, Options{Dir: dir, InitialTotal: 999})
	if got := s2.Budgets(); len(got) != 2 || got[1].Amount != 300 {
		t.Errorf("Budgets() after restart = %v", got)
	}
	if !strings.Contains(logs2.String(), "seeds ignored") {
		t.Errorf("logs = %q", logs2.String())
	}
}

func TestOpenWithoutSeedsCreatesEmptyStore(t *testing.T) {
	dir := t.TempDir()
	s, _ := open(t, Options{Dir: dir})
	if len(s.Budgets()) != 0 || len(s.Mappings()) != 0 {
		t.Errorf("store not empty: %v %v", s.Budgets(), s.Mappings())
	}
	if got := readFile(t, dir); !strings.Contains(got, `"version": 1`) {
		t.Errorf("file = %s", got)
	}
}

func TestOpenErrors(t *testing.T) {
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	write := func(content string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	budget := func(scope, project, amount, currency string) string {
		return `{"version":1,"budgets":[{"scope":"` + scope + `","project":"` + project +
			`","amount":` + amount + `,"currency":"` + currency + `"}]}`
	}
	mapping := func(id, project string) string {
		return `{"version":1,"project_mappings":[{"project_id":"` + id + `","project":"` + project + `"}]}`
	}
	cases := map[string]string{
		"missing dir":       filepath.Join(t.TempDir(), "absent"),
		"not a directory":   notDir,
		"corrupt":           write(`{"version":1,"budgets":[`),
		"trailing data":     write(`{"version":1} {}`),
		"empty file":        write(``),
		"wrong version":     write(`{"version":2}`),
		"no version":        write(`{}`),
		"bad scope":         write(budget("all", "", "1", "EUR")),
		"project on total":  write(budget("total", "acme", "1", "EUR")),
		"missing project":   write(budget("project", "", "1", "EUR")),
		"bad project":       write(budget("project", "Acme", "1", "EUR")),
		"zero amount":       write(budget("total", "", "0", "EUR")),
		"negative amount":   write(budget("total", "", "-1", "EUR")),
		"too big amount":    write(budget("total", "", "100001", "EUR")),
		"bad currency":      write(budget("total", "", "1", "USD")),
		"duplicate budget":  write(`{"version":1,"budgets":[{"scope":"total","amount":1,"currency":"EUR"},{"scope":"total","amount":2,"currency":"EUR"}]}`),
		"bad project_id":    write(mapping("ACME", "acme")),
		"upper project_id":  write(mapping(strings.ToUpper("0000000a-0000-0000-0000-000000000001"), "acme")),
		"bad mapping":       write(mapping(tID1, "big suite")),
		"duplicate mapping": write(`{"version":1,"project_mappings":[{"project_id":"` + tID1 + `","project":"a"},{"project_id":"` + tID1 + `","project":"b"}]}`),
		"oversized":         write(`{"version":1,"x":"` + strings.Repeat("a", maxFileBytes) + `"}`),
		"empty dir name":    "",
		"unknown key":       write(`{"version":1,"budget":[{"scope":"total","amount":1,"currency":"EUR"}]}`),
		"unknown entry key": write(`{"version":1,"budgets":[{"scope":"total","amount":1,"currency":"EUR","amout":2}]}`),
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Open(Options{Dir: dir, Logger: slog.New(slog.DiscardHandler)}); err == nil {
				t.Error("Open succeeded")
			}
		})
	}
}

func TestOpenUnreadableFileAndReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Dir: dir, Logger: slog.New(slog.DiscardHandler)}); err == nil {
		t.Error("Open succeeded on an unreadable file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: a read-only directory, on purpose
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // G302: restore the directory
	// A read-only directory is fine for metrics only, not for the API.
	if _, err := Open(Options{Dir: dir, Logger: slog.New(slog.DiscardHandler)}); err != nil {
		t.Errorf("read-only Open: %v", err)
	}
	if _, err := Open(Options{Dir: dir, RequireWritable: true, Logger: slog.New(slog.DiscardHandler)}); err == nil {
		t.Error("Open succeeded on a read-only directory with RequireWritable")
	}
}

func TestSetAndUnsetBudget(t *testing.T) {
	dir := t.TempDir()
	s, _ := open(t, Options{Dir: dir})
	old, cur, err := s.SetBudget(ScopeProject, "acme", 300)
	if err != nil || old != nil || cur.Amount != 300 || cur.Project != "acme" || cur.Currency != Currency || !cur.UpdatedAt.Equal(tNow) {
		t.Fatalf("SetBudget = %v, %v, %v", old, cur, err)
	}
	old, cur, err = s.SetBudget(ScopeProject, "acme", 350.5)
	if err != nil || old == nil || old.Amount != 300 || cur.Amount != 350.5 {
		t.Fatalf("second SetBudget = %v, %v, %v", old, cur, err)
	}
	if got := readFile(t, dir); !strings.Contains(got, `"amount": 350.5`) {
		t.Errorf("file = %s", got)
	}
	removed, err := s.UnsetBudget(ScopeProject, "acme")
	if err != nil || removed.Amount != 350.5 {
		t.Fatalf("UnsetBudget = %v, %v", removed, err)
	}
	if _, err := s.UnsetBudget(ScopeProject, "acme"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second UnsetBudget err = %v, want ErrNotFound", err)
	}
	if len(s.Budgets()) != 0 || strings.Contains(readFile(t, dir), "acme") {
		t.Errorf("budget still present: %v", s.Budgets())
	}
}

func TestSetBudgetRejectsInvalidInput(t *testing.T) {
	s, _ := open(t, Options{Dir: t.TempDir()})
	for _, c := range []struct {
		scope, project string
		amount         float64
	}{
		{"all", "", 1},
		{ScopeTotal, "acme", 1},
		{ScopeProject, "", 1},
		{ScopeProject, "Acme", 1},
		{ScopeTotal, "", 0},
		{ScopeTotal, "", 100000.01},
	} {
		var inv *InvalidError
		if _, _, err := s.SetBudget(c.scope, c.project, c.amount); !errors.As(err, &inv) {
			t.Errorf("SetBudget(%q, %q, %v) err = %v, want InvalidError", c.scope, c.project, c.amount, err)
		}
		if _, err := s.UnsetBudget(c.scope, c.project); c.amount != 0 && c.amount < MaxAmount && !errors.As(err, &inv) {
			t.Errorf("UnsetBudget(%q, %q) err = %v, want InvalidError", c.scope, c.project, err)
		}
	}
}

func TestSetAndUnsetMapping(t *testing.T) {
	dir := t.TempDir()
	s, _ := open(t, Options{Dir: dir})
	old, cur, err := s.SetMapping(strings.ToUpper(tID1), "acme")
	if err != nil || old != nil || cur.ProjectID != tID1 || cur.Project != "acme" {
		t.Fatalf("SetMapping = %v, %v, %v", old, cur, err)
	}
	old, _, err = s.SetMapping(tID1, "orbit")
	if err != nil || old == nil || old.Project != "acme" {
		t.Fatalf("second SetMapping = %v, %v", old, err)
	}
	if got := readFile(t, dir); !strings.Contains(got, `"project": "orbit"`) {
		t.Errorf("file = %s", got)
	}
	var inv *InvalidError
	if _, _, err := s.SetMapping("acme", "acme"); !errors.As(err, &inv) {
		t.Errorf("bad project_id err = %v", err)
	}
	if _, _, err := s.SetMapping(tID2, "Big Suite"); !errors.As(err, &inv) {
		t.Errorf("bad project err = %v", err)
	}
	if _, err := s.UnsetMapping("x"); !errors.As(err, &inv) {
		t.Errorf("bad unset err = %v", err)
	}
	removed, err := s.UnsetMapping(tID1)
	if err != nil || removed.Project != "orbit" {
		t.Fatalf("UnsetMapping = %v, %v", removed, err)
	}
	if _, err := s.UnsetMapping(tID1); !errors.Is(err, ErrNotFound) {
		t.Errorf("second UnsetMapping err = %v", err)
	}
}

func TestLimits(t *testing.T) {
	s, _ := open(t, Options{Dir: t.TempDir()})
	if s.maxEntries != MaxEntries {
		t.Fatalf("maxEntries = %d, want %d", s.maxEntries, MaxEntries)
	}
	s.maxEntries = 3
	for i := range 3 {
		if _, _, err := s.SetBudget(ScopeProject, fmt.Sprintf("p%d", i), 1); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SetMapping(fmt.Sprintf("00000000-0000-0000-0000-%012d", i), "p"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.SetBudget(ScopeProject, "one-more", 1); !errors.Is(err, ErrLimit) {
		t.Errorf("budget over the limit err = %v", err)
	}
	if _, _, err := s.SetMapping(tID1[:24]+"999999999999", "p"); !errors.Is(err, ErrLimit) {
		t.Errorf("mapping over the limit err = %v", err)
	}
	// Updating an existing entry is still allowed at the limit.
	if _, _, err := s.SetBudget(ScopeProject, "p0", 2); err != nil {
		t.Errorf("update at the limit: %v", err)
	}
}

func TestFailedSaveKeepsState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	dir := t.TempDir()
	s, _ := open(t, Options{Dir: dir, InitialTotal: 250})
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: a read-only directory, on purpose
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // G302: restore the directory
	if _, _, err := s.SetBudget(ScopeTotal, "", 300); err == nil {
		t.Fatal("SetBudget succeeded in a read-only directory")
	}
	if got := s.Budgets(); len(got) != 1 || got[0].Amount != 250 {
		t.Errorf("Budgets() after a failed save = %v", got)
	}
	if _, err := s.UnsetBudget(ScopeTotal, ""); err == nil {
		t.Fatal("UnsetBudget succeeded in a read-only directory")
	}
	if len(s.Budgets()) != 1 {
		t.Errorf("budget removed after a failed save")
	}
}

func TestNoTempFilesLeft(t *testing.T) {
	dir := t.TempDir()
	s, _ := open(t, Options{Dir: dir})
	for i := range 5 {
		if _, _, err := s.SetBudget(ScopeTotal, "", float64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		t.Errorf("directory holds %v", entries)
	}
}

func TestConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	s, _ := open(t, Options{Dir: dir})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.SetBudget(ScopeProject, fmt.Sprintf("p%d", i), float64(i+1)); err != nil {
				t.Error(err)
			}
			_ = s.Budgets()
		}()
	}
	wg.Wait()
	s2, _ := open(t, Options{Dir: dir})
	if got := len(s2.Budgets()); got != 20 {
		t.Errorf("budgets after reload = %d, want 20", got)
	}
}

func TestOpenRefusesNonRegularFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := t.TempDir()
	if err := os.Symlink(target, filepath.Join(linked, FileName)); err != nil {
		t.Fatal(err)
	}
	asDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(asDir, FileName), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"symlink": linked, "directory": asDir} {
		if _, err := Open(Options{Dir: dir}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOpenSortsTidiesAndWarns(t *testing.T) {
	dir := t.TempDir()
	content := `{"version":1,"budgets":[` +
		`{"scope":"total","amount":1,"currency":"EUR"},` +
		`{"scope":"project","project":"zeta","amount":1,"currency":"EUR"},` +
		`{"scope":"project","project":"acme","amount":1,"currency":"EUR"}],` +
		`"project_mappings":[{"project_id":"` + tID2 + `","project":"b"},{"project_id":"` + tID1 + `","project":"a"}]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil { //nolint:gosec // G306: the test needs a too-open file
		t.Fatal(err)
	}
	leftover := filepath.Join(dir, ".budgets-123.tmp")
	if err := os.WriteFile(leftover, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, logs := open(t, Options{Dir: dir})
	var got []string
	for _, b := range s.Budgets() {
		got = append(got, b.Scope+"/"+b.Project)
	}
	if strings.Join(got, ",") != "project/acme,project/zeta,total/" {
		t.Errorf("Budgets() order = %v", got)
	}
	if m := s.Mappings(); m[0].ProjectID != tID1 {
		t.Errorf("Mappings() order = %v", m)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("leftover temporary file kept: %v", err)
	}
	if !strings.Contains(logs.String(), "expected 0600") {
		t.Errorf("no warning for a 0644 file: %s", logs.String())
	}
}
