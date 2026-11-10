package httpx

import (
	"errors"
	"net/http"
	"os"
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
	if _, err := c.GetJSON(t.Context(), "/x", nil, &out); err == nil {
		t.Fatal("want dial guard error")
	}
}
