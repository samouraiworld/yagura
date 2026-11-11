package testkit

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func do(t *testing.T, method, url string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestHitsAndFixtures(t *testing.T) {
	srv := NewServer(t, map[string]Route{
		"/a":           {Body: `{"x":1}`},
		"GET /b?q=1":   {Status: 201, Body: "{}", Header: http.Header{"X-Test": {"v"}}},
		"GET /missing": {Status: 404, Body: "nope"},
	})
	for _, p := range []string{"/a", "/a", "/b?q=1"} {
		do(t, http.MethodGet, srv.URL+p)
	}
	if got := Hits(srv, "/a"); got != 2 {
		t.Errorf("hits /a = %d, want 2", got)
	}
	if got := Hits(srv, "GET /b?q=1"); got != 1 {
		t.Errorf("hits /b = %d, want 1", got)
	}
	if got := Hits(srv, "/missing"); got != 0 {
		t.Errorf("hits /missing = %d, want 0", got)
	}
}

// TestReportsMisuse re-executes the test binary: the child makes a POST and a
// request to an unknown path, which must fail the child and name both.
func TestReportsMisuse(t *testing.T) {
	if os.Getenv("TESTKIT_MISUSE_CHILD") == "1" {
		srv := NewServer(t, map[string]Route{"/a": {Body: "{}"}})
		if got := do(t, http.MethodPost, srv.URL+"/a"); got != http.StatusMethodNotAllowed {
			t.Errorf("POST status = %d, want 405", got)
		}
		if got := do(t, http.MethodGet, srv.URL+"/unknown"); got != http.StatusInternalServerError {
			t.Errorf("unknown path status = %d, want 500", got)
		}
		return
	}
	cmd := exec.CommandContext(context.Background(), //nolint:gosec // re-executes this test binary
		os.Args[0], "-test.run=^TestReportsMisuse$")
	cmd.Env = append(os.Environ(), "TESTKIT_MISUSE_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child passed, want failure:\n%s", out)
	}
	for _, want := range []string{"unexpected POST /a", "no route for GET /unknown"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("child output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "status = ") {
		t.Errorf("child saw wrong statuses:\n%s", out)
	}
}
