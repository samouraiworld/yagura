package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
)

func TestMain(m *testing.M) {
	SetDialGuard(LoopbackOnly)
	os.Exit(m.Run())
}

type countingTransport struct{ calls int }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, errors.New("must not be called")
}

func TestGuardRejectsNonGet(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD"} {
		next := &countingTransport{}
		g := &getOnlyTransport{next: next}
		req, err := http.NewRequestWithContext(t.Context(), method, "https://example.com/x", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := g.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, ErrMethodNotAllowed) {
			t.Errorf("%s: err = %v, want ErrMethodNotAllowed", method, err)
		}
		if next.calls != 0 {
			t.Errorf("%s: next transport called %d times", method, next.calls)
		}
	}
}

func TestLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80"} {
		if err := LoopbackOnly(addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{"192.0.2.1:443", "not-an-address"} {
		if err := LoopbackOnly(addr); err == nil {
			t.Errorf("%s: want error", addr)
		}
	}
}

func TestDialGuardBlocksNonLoopback(t *testing.T) {
	c := New(Options{Provider: "p", BaseURL: "http://192.0.2.1:9"})
	var out any
	_, err := c.GetJSON(t.Context(), "/x", url.Values{"token": {"secret"}}, &out)
	if !errors.Is(err, errDialBlocked) {
		t.Fatalf("err = %v, want errDialBlocked", err)
	}
}

func TestDialGuardIsWiredIntoTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var mu sync.Mutex
	var seen []string
	SetDialGuard(func(address string) error {
		mu.Lock()
		seen = append(seen, address)
		mu.Unlock()
		return LoopbackOnly(address)
	})
	defer SetDialGuard(LoopbackOnly)

	c := New(Options{Provider: "p", BaseURL: srv.URL})
	var out any
	if _, err := c.GetJSON(t.Context(), "/x", nil, &out); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(seen, srv.Listener.Addr().String()) {
		t.Errorf("guard saw %v, want %s", seen, srv.Listener.Addr())
	}
}

func TestNewPutsGetGuardInFrontOfTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server reached by non-GET request")
	}))
	defer srv.Close()
	c := New(Options{Provider: "p", BaseURL: srv.URL})
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD"} {
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.http.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, ErrMethodNotAllowed) {
			t.Errorf("%s: err = %v, want ErrMethodNotAllowed", method, err)
		}
	}
}
