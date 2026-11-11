package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func tokenFn(v string) func() (string, error) {
	return func() (string, error) { return v, nil }
}

func TestGetJSONSendsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if !strings.HasPrefix(r.UserAgent(), "yagura/") {
			t.Errorf("user agent = %q", r.UserAgent())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t" {
			t.Errorf("authorization = %q", got)
		}
		if r.URL.Query().Get("page") != "2" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		w.Header().Set("X-Test", "yes")
		_, _ = fmt.Fprint(w, `{"a":1,"unknown":true}`)
	}))
	defer srv.Close()

	c := New(Options{Provider: "p", BaseURL: srv.URL, Auth: BearerAuth(tokenFn("t"))})
	var out struct{ A int }
	h, err := c.GetJSON(t.Context(), "/v1/x", url.Values{"page": {"2"}}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.A != 1 {
		t.Errorf("A = %d", out.A)
	}
	if h.Get("X-Test") != "yes" {
		t.Errorf("headers not returned: %v", h)
	}
}

func TestHeaderAuth(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
	if err := HeaderAuth("X-Key", tokenFn("k"))(req); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Key") != "k" {
		t.Errorf("header = %v", req.Header)
	}
}

func TestGetJSONStatusErrors(t *testing.T) {
	for _, tc := range []struct {
		code       int
		auth, miss bool
	}{{401, true, false}, {403, true, false}, {404, false, true}, {500, false, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = fmt.Fprint(w, "token=secret in body")
		}))
		c := New(Options{Provider: "p", BaseURL: srv.URL})
		var out any
		_, err := c.GetJSON(t.Context(), "/v1/x", url.Values{"token": {"secret"}}, &out)
		srv.Close()
		if err == nil {
			t.Fatalf("%d: want error", tc.code)
		}
		if IsAuthError(err) != tc.auth || IsNotFound(err) != tc.miss {
			t.Errorf("%d: auth=%v notfound=%v", tc.code, IsAuthError(err), IsNotFound(err))
		}
		if want := fmt.Sprintf("GET /v1/x: HTTP %d", tc.code); err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	}
}

func TestGetJSONRejectsNonJSON(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want error
	}{
		{"html", []byte("<html>nope</html>"), ErrDecode},
		{"truncated", []byte(`{"a":`), ErrDecode},
		{"too large", bytes.Repeat([]byte(" "), 11<<20), ErrBodyTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(tc.body)
			}))
			defer srv.Close()
			c := New(Options{Provider: "p", BaseURL: srv.URL})
			var out any
			if _, err := c.GetJSON(t.Context(), "/x", nil, &out); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAuthErrorStopsRequest(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	boom := errors.New("no token")
	c := New(Options{Provider: "p", BaseURL: srv.URL, Auth: func(*http.Request) error { return boom }})
	var out any
	if _, err := c.GetJSON(t.Context(), "/x", nil, &out); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if hit {
		t.Error("request was sent")
	}
}

func TestRequestsCounter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(404)
			return
		}
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	counter := NewRequestsCounter()
	c := New(Options{Provider: "p", BaseURL: srv.URL, Requests: counter})
	var out any
	_, _ = c.GetJSON(t.Context(), "/ok", nil, &out)
	_, _ = c.GetJSON(t.Context(), "/missing", nil, &out)
	if v := testutil.ToFloat64(counter.WithLabelValues("p", "200")); v != 1 {
		t.Errorf("200 = %v", v)
	}
	if v := testutil.ToFloat64(counter.WithLabelValues("p", "404")); v != 1 {
		t.Errorf("404 = %v", v)
	}
}

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestTransportErrorHidesQueryAndCounts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // transport timeouts are retried; keep the waits virtual
		counter := NewRequestsCounter()
		c := newClient(Options{Provider: "p", BaseURL: "https://example.com", Requests: counter, Auth: BearerAuth(tokenFn("hunter2"))},
			failingTransport{err: context.DeadlineExceeded})
		var out any
		_, err := c.GetJSON(t.Context(), "/v1/x", url.Values{"token": {"secret"}}, &out)
		if err == nil {
			t.Fatal("want error")
		}
		for _, leak := range []string{"secret", "token=", "hunter2", "?"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("error %q leaks %q", err, leak)
			}
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("errors.Is lost the cause: %v", err)
		}
		if v := testutil.ToFloat64(counter.WithLabelValues("p", "error")); v != maxAttempts {
			t.Errorf("error count = %v, want %d (one per attempt)", v, maxAttempts)
		}
	})
}

func TestTransportErrorWithURLInMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // network errors are retried; keep the waits virtual
		c := newClient(Options{Provider: "p", BaseURL: "https://example.com"},
			failingTransport{err: errors.New("dial failed")})
		var out any
		_, err := c.GetJSON(t.Context(), "/v1/x", url.Values{"token": {"secret"}}, &out)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRedirectIsNotFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	c := New(Options{Provider: "p", BaseURL: srv.URL})
	var out any
	_, err := c.GetJSON(t.Context(), "/x", nil, &out)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 302 {
		t.Errorf("err = %v", err)
	}
}

type statusTransport struct{ code int }

func (s statusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: s.code, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
}

func TestRequestsCounterBoundsCodeLabel(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{
		{404, "404"}, {100, "100"}, {599, "599"},
		{999, "invalid"}, {600, "invalid"}, {99, "invalid"}, {0, "invalid"}, {-5, "invalid"},
	} {
		counter := NewRequestsCounter()
		c := newClient(Options{Provider: "p", BaseURL: "https://example.com", Requests: counter}, statusTransport{code: tc.code})
		var out any
		_, _ = c.GetJSON(t.Context(), "/x", nil, &out)
		if v := testutil.ToFloat64(counter.WithLabelValues("p", tc.want)); v != 1 {
			t.Errorf("status %d: code=%q count = %v, want 1", tc.code, tc.want, v)
		}
		if n := testutil.CollectAndCount(counter); n != 1 {
			t.Errorf("status %d: %d series, want 1", tc.code, n)
		}
	}
}
