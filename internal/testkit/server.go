// Package testkit holds helpers shared by the provider tests. It is imported
// from _test files only.
package testkit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// Route is the canned answer of one request key. Body wins over File, which
// is read from the calling package's directory (usually testdata/...).
type Route struct {
	Status int // 0 means 200
	File   string
	Body   string
	Header http.Header
}

var (
	hitsMu sync.Mutex
	hits   = map[*httptest.Server]map[string]int{}
)

// NewServer starts an httptest server answering routes. A key is either
// "GET /path?query" or "/path" (an alias for the GET form). A request that is
// not a GET, or whose method, path and query match no route, is reported with
// t.Errorf and answered with 405 or 500. Every hit is counted under its key.
func NewServer(t *testing.T, routes map[string]Route) *httptest.Server {
	t.Helper()
	table := make(map[string]Route, len(routes))
	for k, r := range routes {
		if !strings.HasPrefix(k, "GET ") {
			k = "GET " + k
		}
		table[k] = r
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("testkit: unexpected %s %s", r.Method, r.URL.RequestURI())
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := "GET " + r.URL.RequestURI()
		route, ok := table[key]
		if !ok {
			key = "GET " + r.URL.Path
			route, ok = table[key]
		}
		if !ok {
			t.Errorf("testkit: no route for GET %s", r.URL.RequestURI())
			http.Error(w, "no route", http.StatusInternalServerError)
			return
		}
		hitsMu.Lock()
		hits[srv][key]++
		hitsMu.Unlock()
		body := []byte(route.Body)
		if route.Body == "" && route.File != "" {
			b, err := os.ReadFile(route.File)
			if err != nil {
				t.Errorf("testkit: %v", err)
				http.Error(w, "fixture unreadable", http.StatusInternalServerError)
				return
			}
			body = b
		}
		for k, vs := range route.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		status := route.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	hitsMu.Lock()
	hits[srv] = map[string]int{}
	hitsMu.Unlock()
	t.Cleanup(func() {
		srv.Close()
		hitsMu.Lock()
		delete(hits, srv)
		hitsMu.Unlock()
	})
	return srv
}

// Hits returns how many requests were answered for key, written like a route
// key ("GET /path?query" or "/path").
func Hits(srv *httptest.Server, key string) int {
	if !strings.HasPrefix(key, "GET ") {
		key = "GET " + key
	}
	hitsMu.Lock()
	defer hitsMu.Unlock()
	return hits[srv][key]
}
