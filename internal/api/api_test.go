package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samouraiworld/yagura/internal/budget"
	"github.com/samouraiworld/yagura/internal/scaleway"
)

const (
	idAcme  = "00000000-0000-0000-0000-000000000001"
	idSuite = "00000000-0000-0000-0000-000000000002"
	idOther = "00000000-0000-0000-0000-0000000000ff"
)

type fixture struct {
	h     http.Handler
	token string
	store *budget.Store
	dir   string
	logs  *bytes.Buffer
}

func fakeToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{token: fakeToken(t), dir: t.TempDir(), logs: &bytes.Buffer{}}
	logger := slog.New(slog.NewTextHandler(f.logs, nil))
	var err error
	f.store, err = budget.Open(budget.Options{Dir: f.dir, InitialTotal: 250, InitialScaleway: 250, Logger: logger,
		Now: func() time.Time { return time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	projects := []scaleway.Project{{ID: idAcme, Name: "acme-backups"}, {ID: idSuite, Name: "Big Suite"}}
	f.h = Handler(Options{
		Store:    f.store,
		Projects: func() []scaleway.Project { return projects },
		Token:    func() (string, error) { return f.token, nil },
		Logger:   logger,
	})
	return f
}

type reply struct {
	code   int
	header http.Header
	body   map[string]any
	raw    string
}

func (f *fixture) do(t *testing.T, method, target, contentType, body string) reply {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:4321"
	req.Header.Set("Authorization", "Bearer "+f.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return serve(t, f.h, req)
}

func serve(t *testing.T, h http.Handler, req *http.Request) reply {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r := reply{code: rec.Code, header: rec.Header(), raw: rec.Body.String()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &r.body); err != nil {
			t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
		}
	}
	return r
}

func TestAuth(t *testing.T) {
	f := newFixture(t)
	for name, header := range map[string]string{
		"missing":      "",
		"wrong token":  "Bearer " + fakeToken(t),
		"basic scheme": "Basic " + f.token,
		"no token":     "Bearer ",
		"prefix only":  "Bearer " + f.token[:10],
		"token only":   f.token,
	} {
		for _, target := range []string{"/api/v1/budgets", "/api/v1/unknown", "/api/"} {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			r := serve(t, f.h, req)
			if r.code != http.StatusUnauthorized || r.body["error"] != "unauthorized" || r.header.Get("WWW-Authenticate") == "" {
				t.Errorf("%s %s: %d %v", name, target, r.code, r.raw)
			}
		}
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/budgets", nil)
	req.Header.Set("Authorization", "bearer  "+f.token+" ")
	if r := serve(t, f.h, req); r.code != http.StatusOK {
		t.Errorf("case-insensitive scheme: %d %s", r.code, r.raw)
	}
	for _, target := range []string{"/", "/metrics", "/api", "/apix/v1/budgets"} {
		if r := serve(t, f.h, httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)); r.code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, r.code)
		}
	}
	if strings.Contains(f.logs.String(), f.token) || strings.Contains(f.logs.String(), "Bearer") {
		t.Errorf("logs leak the token or the header: %s", f.logs.String())
	}
	if !strings.Contains(f.logs.String(), "remote_addr=") {
		t.Errorf("auth failures are not logged: %s", f.logs.String())
	}
}

func TestTokenReadError(t *testing.T) {
	f := newFixture(t)
	h := Handler(Options{Store: f.store, Token: func() (string, error) { return "", errors.New("YAGURA_API_TOKEN_FILE: cannot read") }, Logger: slog.New(slog.DiscardHandler)})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/budgets", nil)
	req.Header.Set("Authorization", "Bearer x")
	if r := serve(t, h, req); r.code != http.StatusInternalServerError {
		t.Errorf("code = %d", r.code)
	}
}

func TestCheckToken(t *testing.T) {
	if err := CheckToken(func() (string, error) { return strings.Repeat("a", MinTokenLength), nil }); err != nil {
		t.Error(err)
	}
	if err := CheckToken(func() (string, error) { return strings.Repeat("a", MinTokenLength-1), nil }); err == nil {
		t.Error("short token accepted")
	}
	if err := CheckToken(func() (string, error) { return "", errors.New("unreadable") }); err == nil {
		t.Error("read error accepted")
	}
}

func TestListBudgets(t *testing.T) {
	f := newFixture(t)
	r := f.do(t, http.MethodGet, "/api/v1/budgets", "", "")
	if r.code != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	want := `{"budgets":[{"scope":"scaleway","project":"","amount":250,"currency":"EUR","updated_at":"2026-11-20T10:00:00Z"},{"scope":"total","project":"","amount":250,"currency":"EUR","updated_at":"2026-11-20T10:00:00Z"}]}` + "\n"
	if r.raw != want {
		t.Errorf("body = %s\nwant %s", r.raw, want)
	}
}

func TestSetBudgetParameterSources(t *testing.T) {
	cases := []struct {
		name, target, contentType, body string
		want                            float64
	}{
		{"query", "/api/v1/budgets/set?scope=project&project=acme&amount=300", "", "", 300},
		{"query comma", "/api/v1/budgets/set?scope=project&project=acme&amount=300%2C5", "", "", 300.5},
		{"json number", "/api/v1/budgets/set", "application/json", `{"scope":"project","project":"acme","amount":300.5}`, 300.5},
		{"json string comma", "/api/v1/budgets/set", "application/json; charset=utf-8", `{"scope":"project","project":"acme","amount":"300,5"}`, 300.5},
		{"json null ignored", "/api/v1/budgets/set?amount=12", "application/json", `{"scope":"project","project":"acme","amount":null}`, 12},
		{"form", "/api/v1/budgets/set", "application/x-www-form-urlencoded", "scope=project&project=acme&amount=300.5", 300.5},
		{"sniffed json", "/api/v1/budgets/set", "", ` {"scope":"project","project":"acme","amount":"42"}`, 42},
		{"sniffed form", "/api/v1/budgets/set", "text/plain", "scope=project&project=acme&amount=43", 43},
		{"mixed same value", "/api/v1/budgets/set?scope=project", "application/json", `{"scope":"project","project":"acme","amount":7}`, 7},
		{"currency EUR", "/api/v1/budgets/set?currency=eur", "application/json", `{"scope":"project","project":"acme","amount":9}`, 9},
		{"spaces trimmed", "/api/v1/budgets/set", "application/json", `{"scope":" project ","project":" acme ","amount":" 8 "}`, 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			r := f.do(t, http.MethodPost, c.target, c.contentType, c.body)
			if r.code != http.StatusOK || r.body["new_amount"] != c.want {
				t.Fatalf("%d %s", r.code, r.raw)
			}
			b := f.store.Budgets()
			if len(b) != 3 || b[0].Project != "acme" || b[0].Amount != c.want {
				t.Errorf("store = %v", b)
			}
		})
	}
}

func TestSetBudgetResponseAndLog(t *testing.T) {
	f := newFixture(t)
	r := f.do(t, http.MethodPost, "/api/v1/budgets/set?scope=total&amount=300", "", "")
	if r.code != http.StatusOK {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	want := map[string]any{
		"action": "budget_set", "scope": "total", "project": "", "currency": "EUR",
		"old_amount": 250.0, "new_amount": 300.0,
		"message": "budget total set to 300 EUR (was 250 EUR)", "warnings": []any{},
	}
	if js, wjs := mustJSON(t, r.body), mustJSON(t, want); js != wjs {
		t.Errorf("body = %s\nwant %s", js, wjs)
	}
	logs := f.logs.String()
	for _, s := range []string{`msg="budget changed"`, "action=budget_set", "scope=total", "old_amount=250", "new_amount=300", "remote_addr=192.0.2.10:4321"} {
		if !strings.Contains(logs, s) {
			t.Errorf("log misses %q: %s", s, logs)
		}
	}
	if strings.Contains(logs, f.token) {
		t.Error("log leaks the token")
	}

	r = f.do(t, http.MethodPost, "/api/v1/budgets/set?scope=project&project=acme&amount=10", "", "")
	if r.body["old_amount"] != nil || r.body["message"] != "budget project acme set to 10 EUR (new)" || len(r.body["warnings"].([]any)) != 0 {
		t.Errorf("new project budget: %s", r.raw)
	}
	r = f.do(t, http.MethodPost, "/api/v1/budgets/set?scope=project&project=zeta&amount=10", "", "")
	if r.code != http.StatusOK || len(r.body["warnings"].([]any)) != 1 {
		t.Errorf("budget without Scaleway project: %s", r.raw)
	}
}

func TestSetBudgetRejects(t *testing.T) {
	f := newFixture(t)
	cases := map[string]struct {
		target, contentType, body string
		code                      int
	}{
		"no amount":           {"/api/v1/budgets/set?scope=total", "", "", 400},
		"other currency":      {"/api/v1/budgets/set?scope=total&amount=1&currency=USD", "", "", 400},
		"zero":                {"/api/v1/budgets/set?scope=total&amount=0", "", "", 400},
		"negative":            {"/api/v1/budgets/set?scope=total&amount=-1", "", "", 400},
		"over cap":            {"/api/v1/budgets/set?scope=total&amount=100001", "", "", 400},
		"exponent":            {"/api/v1/budgets/set", "application/json", `{"scope":"total","amount":1e3}`, 400},
		"bad scope":           {"/api/v1/budgets/set?scope=all&amount=1", "", "", 400},
		"no scope":            {"/api/v1/budgets/set?amount=1", "", "", 400},
		"bad project":         {"/api/v1/budgets/set?scope=project&project=Big+Suite&amount=1", "", "", 400},
		"project on total":    {"/api/v1/budgets/set?scope=total&project=acme&amount=1", "", "", 400},
		"conflict":            {"/api/v1/budgets/set?amount=2", "application/json", `{"scope":"total","amount":3}`, 400},
		"duplicate query":     {"/api/v1/budgets/set?scope=total&amount=2&amount=3", "", "", 400},
		"bad query escape":    {"/api/v1/budgets/set?scope=total&amount=%zz", "", "", 400},
		"bad json":            {"/api/v1/budgets/set", "application/json", `{"scope":`, 400},
		"json array":          {"/api/v1/budgets/set", "application/json", `[1]`, 400},
		"json trailing":       {"/api/v1/budgets/set", "application/json", `{"scope":"total","amount":1} {}`, 400},
		"json bool":           {"/api/v1/budgets/set", "application/json", `{"scope":"total","amount":true}`, 400},
		"json object":         {"/api/v1/budgets/set", "application/json", `{"scope":{"a":1},"amount":1}`, 400},
		"bad form":            {"/api/v1/budgets/set", "application/x-www-form-urlencoded", "scope=%zz", 400},
		"unsupported type":    {"/api/v1/budgets/set", "application/xml", "<a/>", 415},
		"bad content type":    {"/api/v1/budgets/set", "a/b; =", "x", 415},
		"too large":           {"/api/v1/budgets/set", "application/json", `{"scope":"total","amount":1,"x":"` + strings.Repeat("a", maxBodyBytes) + `"}`, 413},
		"GET on a write":      {"/api/v1/budgets/set?scope=total&amount=1", "", "", 405},
		"POST on a list":      {"/api/v1/budgets", "", "", 405},
		"unknown route":       {"/api/v1/budgets/delete", "", "", 404},
		"unset without scope": {"/api/v1/budgets/unset", "", "", 400},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			method := http.MethodPost
			if name == "GET on a write" {
				method = http.MethodGet
			}
			r := f.do(t, method, c.target, c.contentType, c.body)
			if r.code != c.code {
				t.Errorf("code = %d, want %d (%s)", r.code, c.code, r.raw)
			}
			if c.code == 400 && (r.body["error"] == nil || r.body["error"] == "") {
				t.Errorf("400 without an error message: %s", r.raw)
			}
		})
	}
	if b := f.store.Budgets(); len(b) != 2 || b[0].Amount != 250 || b[1].Amount != 250 {
		t.Errorf("store changed: %v", b)
	}
}

func TestUnsetBudget(t *testing.T) {
	f := newFixture(t)
	r := f.do(t, http.MethodPost, "/api/v1/budgets/unset", "application/json", `{"scope":"scaleway"}`)
	if r.code != http.StatusOK || r.body["old_amount"] != 250.0 || r.body["new_amount"] != nil ||
		r.body["message"] != "budget scaleway removed (was 250 EUR)" || r.body["action"] != "budget_unset" {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	if r := f.do(t, http.MethodPost, "/api/v1/budgets/unset?scope=scaleway", "", ""); r.code != http.StatusNotFound || r.body["error"] == nil {
		t.Errorf("second unset: %d %s", r.code, r.raw)
	}
	if !strings.Contains(f.logs.String(), "action=budget_unset") {
		t.Errorf("no log line: %s", f.logs.String())
	}
}

func TestProjects(t *testing.T) {
	f := newFixture(t)
	r := f.do(t, http.MethodGet, "/api/v1/projects", "", "")
	want := `{"projects":[{"project_id":"` + idAcme + `","project_name":"acme-backups","project":"acme","mapping":"auto","in_billing":true,"in_history":false},` +
		`{"project_id":"` + idSuite + `","project_name":"Big Suite","project":"big-suite","mapping":"auto","in_billing":true,"in_history":false}]}` + "\n"
	if r.code != http.StatusOK || r.raw != want {
		t.Fatalf("%d %s\nwant %s", r.code, r.raw, want)
	}

	r = f.do(t, http.MethodPost, "/api/v1/projects/set", "application/x-www-form-urlencoded", "project_id="+strings.ToUpper(idSuite)+"&project=suite")
	wantSet := map[string]any{
		"action": "project_set", "project_id": idSuite, "project_name": "Big Suite",
		"old_project": "big-suite", "old_mapping": "auto", "new_project": "suite", "new_mapping": "manual",
		"message": "Scaleway project Big Suite now maps to suite (was big-suite, auto)", "warnings": []any{},
	}
	if r.code != http.StatusOK || mustJSON(t, r.body) != mustJSON(t, wantSet) {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	r = f.do(t, http.MethodPost, "/api/v1/projects/set?project_id="+idOther+"&project=orbit", "", "")
	if r.code != http.StatusOK || r.body["old_project"] != nil || len(r.body["warnings"].([]any)) != 1 {
		t.Errorf("unseen project: %d %s", r.code, r.raw)
	}
	r = f.do(t, http.MethodGet, "/api/v1/projects", "", "")
	if !strings.Contains(r.raw, `{"project_id":"`+idOther+`","project_name":"","project":"orbit","mapping":"manual","in_billing":false,"in_history":false}`) {
		t.Errorf("list after set: %s", r.raw)
	}

	r = f.do(t, http.MethodPost, "/api/v1/projects/unset?project_id="+idSuite, "", "")
	wantUnset := map[string]any{
		"action": "project_unset", "project_id": idSuite, "project_name": "Big Suite",
		"old_project": "suite", "old_mapping": "manual", "new_project": "big-suite", "new_mapping": "auto",
		"message": "Scaleway project Big Suite now maps to big-suite (was suite, manual)", "warnings": []any{},
	}
	if r.code != http.StatusOK || mustJSON(t, r.body) != mustJSON(t, wantUnset) {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	r = f.do(t, http.MethodPost, "/api/v1/projects/unset?project_id="+idOther, "", "")
	if r.code != http.StatusOK || r.body["new_project"] != nil || r.body["new_mapping"] != nil {
		t.Errorf("unset unseen: %d %s", r.code, r.raw)
	}
	if r := f.do(t, http.MethodPost, "/api/v1/projects/unset?project_id="+idSuite, "", ""); r.code != http.StatusNotFound {
		t.Errorf("unset without manual mapping: %d", r.code)
	}
	for _, target := range []string{
		"/api/v1/projects/set?project_id=acme&project=acme",
		"/api/v1/projects/set?project_id=" + idSuite + "&project=Big+Suite",
		"/api/v1/projects/set?project_id=" + idSuite,
		"/api/v1/projects/unset",
	} {
		if r := f.do(t, http.MethodPost, target, "", ""); r.code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", target, r.code)
		}
	}
	logs := f.logs.String()
	for _, s := range []string{`msg="project mapping changed"`, "action=project_set", "action=project_unset", "old_project=big-suite", "new_project=suite"} {
		if !strings.Contains(logs, s) {
			t.Errorf("log misses %q", s)
		}
	}
}

// A project seen only in the monthly history is listed and can be mapped
// like a billed one; an unknown ID keeps the warning.
func TestProjectsWithHistory(t *testing.T) {
	const (
		idOld     = "00000000-0000-0000-0000-0000000000a1"
		idDeleted = "00000000-0000-0000-0000-0000000000d1"
	)
	f := newFixture(t)
	history := []scaleway.Project{{ID: idAcme, Name: "acme-backups"}, {ID: idOld, Name: "Old Tools"}, {ID: idDeleted, Name: idDeleted}}
	f.h = Handler(Options{
		Store: f.store,
		Projects: func() []scaleway.Project {
			return []scaleway.Project{{ID: idAcme, Name: "acme-backups"}, {ID: idSuite, Name: "Big Suite"}}
		},
		History: func() []scaleway.Project { return history },
		Token:   func() (string, error) { return f.token, nil },
		Logger:  slog.New(slog.DiscardHandler),
	})
	r := f.do(t, http.MethodGet, "/api/v1/projects", "", "")
	want := `{"projects":[` +
		`{"project_id":"` + idDeleted + `","project_name":"` + idDeleted + `","project":"` + idDeleted + `","mapping":"auto","in_billing":false,"in_history":true},` +
		`{"project_id":"` + idAcme + `","project_name":"acme-backups","project":"acme","mapping":"auto","in_billing":true,"in_history":true},` +
		`{"project_id":"` + idSuite + `","project_name":"Big Suite","project":"big-suite","mapping":"auto","in_billing":true,"in_history":false},` +
		`{"project_id":"` + idOld + `","project_name":"Old Tools","project":"old-tools","mapping":"auto","in_billing":false,"in_history":true}]}` + "\n"
	if r.code != http.StatusOK || r.raw != want {
		t.Fatalf("%d %s\nwant %s", r.code, r.raw, want)
	}

	r = f.do(t, http.MethodPost, "/api/v1/projects/set?project_id="+idOld+"&project=tools", "", "")
	wantSet := map[string]any{
		"action": "project_set", "project_id": idOld, "project_name": "Old Tools",
		"old_project": "old-tools", "old_mapping": "auto", "new_project": "tools", "new_mapping": "manual",
		"message": "Scaleway project Old Tools now maps to tools (was old-tools, auto)", "warnings": []any{},
	}
	if r.code != http.StatusOK || mustJSON(t, r.body) != mustJSON(t, wantSet) {
		t.Fatalf("set history project: %d %s", r.code, r.raw)
	}
	r = f.do(t, http.MethodPost, "/api/v1/projects/set?project_id="+idDeleted+"&project=legacy", "", "")
	if r.code != http.StatusOK || r.body["old_project"] != idDeleted || len(r.body["warnings"].([]any)) != 0 {
		t.Fatalf("set deleted project: %d %s", r.code, r.raw)
	}
	r = f.do(t, http.MethodGet, "/api/v1/projects", "", "")
	if !strings.Contains(r.raw, `{"project_id":"`+idOld+`","project_name":"Old Tools","project":"tools","mapping":"manual","in_billing":false,"in_history":true}`) {
		t.Errorf("list after set: %s", r.raw)
	}
	r = f.do(t, http.MethodPost, "/api/v1/projects/unset?project_id="+idOld, "", "")
	if r.code != http.StatusOK || r.body["new_project"] != "old-tools" || r.body["new_mapping"] != "auto" {
		t.Errorf("unset history project: %d %s", r.code, r.raw)
	}
	r = f.do(t, http.MethodPost, "/api/v1/projects/set?project_id="+idOther+"&project=orbit", "", "")
	if r.code != http.StatusOK || r.body["old_project"] != nil || len(r.body["warnings"].([]any)) != 1 {
		t.Errorf("unknown project: %d %s", r.code, r.raw)
	}
}

// Grafana's buttons send an extra empty parameter, ignored=. Unknown
// parameters are ignored, whatever their value and wherever they come from.
func TestSetBudgetIgnoresUnknownEmptyParameter(t *testing.T) {
	cases := []struct{ name, target, contentType, body string }{
		{"query", "/api/v1/budgets/set?scope=total&amount=300&ignored=", "", ""},
		{"query on a form body", "/api/v1/budgets/set?ignored=", "application/x-www-form-urlencoded", "scope=total&amount=300"},
		{"form", "/api/v1/budgets/set", "application/x-www-form-urlencoded", "scope=total&amount=300&ignored="},
		{"json", "/api/v1/budgets/set", "application/json", `{"scope":"total","amount":"300","ignored":""}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			r := f.do(t, http.MethodPost, c.target, c.contentType, c.body)
			if r.code != http.StatusOK || r.body["new_amount"] != 300.0 {
				t.Fatalf("%d %s", r.code, r.raw)
			}
		})
	}
}

func TestProjectsWithoutScaleway(t *testing.T) {
	f := newFixture(t)
	h := Handler(Options{Store: f.store, Token: func() (string, error) { return f.token, nil }, Logger: slog.New(slog.DiscardHandler)})
	f.h = h
	if r := f.do(t, http.MethodGet, "/api/v1/projects", "", ""); r.code != http.StatusOK || r.raw != `{"projects":[]}`+"\n" {
		t.Errorf("%d %s", r.code, r.raw)
	}
	if r := f.do(t, http.MethodPost, "/api/v1/budgets/set?scope=project&project=acme&amount=1", "", ""); len(r.body["warnings"].([]any)) != 1 {
		t.Errorf("no warning without Scaleway: %s", r.raw)
	}
}

func TestSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	f := newFixture(t)
	if err := os.Chmod(f.dir, 0o500); err != nil { //nolint:gosec // G302: a read-only directory, on purpose
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.dir, 0o700) }) //nolint:gosec // G302: restore the directory
	r := f.do(t, http.MethodPost, "/api/v1/budgets/set?scope=total&amount=1", "", "")
	if r.code != http.StatusInternalServerError || r.body["error"] == nil {
		t.Errorf("%d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, f.dir) {
		t.Errorf("error leaks the path: %s", r.raw)
	}
}

func TestLimitReached(t *testing.T) {
	dir := t.TempDir()
	var entries []string
	for i := range budget.MaxEntries {
		entries = append(entries, `{"scope":"project","project":"p`+strconv.Itoa(i)+`","amount":1,"currency":"EUR"}`)
	}
	content := `{"version":1,"budgets":[` + strings.Join(entries, ",") + `]}`
	if err := os.WriteFile(filepath.Join(dir, budget.FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := budget.Open(budget.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{token: fakeToken(t)}
	f.h = Handler(Options{Store: store, Token: func() (string, error) { return f.token, nil }, Logger: slog.New(slog.DiscardHandler)})
	if r := f.do(t, http.MethodPost, "/api/v1/budgets/set?scope=project&project=one-more&amount=1", "", ""); r.code != http.StatusBadRequest {
		t.Errorf("over the limit: %d %s", r.code, r.raw)
	}
	if r := f.do(t, http.MethodPost, "/api/v1/budgets/set?scope=project&project=p1&amount=2", "", ""); r.code != http.StatusOK {
		t.Errorf("update at the limit: %d %s", r.code, r.raw)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReviewHardening(t *testing.T) {
	f := newFixture(t)

	// A key repeated in the JSON body with different values is a conflict.
	if r := f.do(t, http.MethodPost, "/api/v1/budgets/set", "application/json", `{"scope":"total","amount":2,"amount":3}`); r.code != http.StatusBadRequest {
		t.Errorf("duplicate JSON key: %d %s", r.code, r.raw)
	}
	if r := f.do(t, http.MethodPost, "/api/v1/budgets/set", "application/json", `{"scope":"total","amount":2,"amount":"2"}`); r.code != http.StatusOK {
		t.Errorf("repeated JSON key with the same value: %d %s", r.code, r.raw)
	}

	// 404 and 405 are JSON; 405 names the allowed methods.
	r := f.do(t, http.MethodGet, "/api/v1/budgets/set", "", "")
	if r.code != http.StatusMethodNotAllowed || r.body["error"] == nil || r.header.Get("Allow") != "POST" {
		t.Errorf("405: %d %v %s", r.code, r.header, r.raw)
	}
	if r := f.do(t, http.MethodDelete, "/api/v1/projects", "", ""); r.code != http.StatusMethodNotAllowed || r.header.Get("Allow") != "GET, HEAD" {
		t.Errorf("405 on a GET route: %d %v", r.code, r.header)
	}
	if r := f.do(t, http.MethodGet, "/api/v1/nothing", "", ""); r.code != http.StatusNotFound || r.body["error"] != "not found" {
		t.Errorf("404: %d %s", r.code, r.raw)
	}
	if r := f.do(t, http.MethodHead, "/api/v1/budgets", "", ""); r.code != http.StatusOK {
		t.Errorf("HEAD: %d", r.code)
	}

	// An oversized Authorization header is refused before hashing.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/budgets", nil)
	req.Header.Set("Authorization", "Bearer "+f.token+strings.Repeat("x", maxAuthHeader))
	if r := serve(t, f.h, req); r.code != http.StatusUnauthorized {
		t.Errorf("oversized header: %d", r.code)
	}
	// The method is capped in the rejection log.
	req = httptest.NewRequestWithContext(context.Background(), strings.Repeat("M", 500), "/api/v1/budgets", nil)
	_ = serve(t, f.h, req)
	if strings.Contains(f.logs.String(), strings.Repeat("M", 100)) {
		t.Error("the method is not capped in the log")
	}
}

func TestTokenProblemsAtRequestTime(t *testing.T) {
	f := newFixture(t)
	for name, token := range map[string]func() (string, error){
		"short":      func() (string, error) { return "short", nil },
		"unreadable": func() (string, error) { return "", errors.New("YAGURA_API_TOKEN_FILE: cannot read") },
	} {
		h := Handler(Options{Store: f.store, Token: token, Logger: slog.New(slog.DiscardHandler)})
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/budgets", nil)
		req.Header.Set("Authorization", "Bearer short")
		if r := serve(t, h, req); r.code != http.StatusInternalServerError {
			t.Errorf("%s token with a header: %d", name, r.code)
		}
		// Without a header the client learns nothing about the token.
		req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/budgets", nil)
		if r := serve(t, h, req); r.code != http.StatusUnauthorized {
			t.Errorf("%s token without a header: %d", name, r.code)
		}
	}
}
