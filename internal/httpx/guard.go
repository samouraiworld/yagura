package httpx

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync/atomic"
	"syscall"
)

// ErrMethodNotAllowed is returned for any request whose method is not GET.
var ErrMethodNotAllowed = errors.New("httpx: only GET requests are allowed")

// getOnlyTransport rejects every method except GET before anything is sent.
type getOnlyTransport struct{ next http.RoundTripper }

func (g *getOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("%w: %s", ErrMethodNotAllowed, req.Method)
	}
	return g.next.RoundTrip(req)
}

// errDialBlocked marks a connection refused by the dial guard.
var errDialBlocked = errors.New("httpx: dial blocked by guard")

var dialGuard atomic.Pointer[func(address string) error]

// SetDialGuard installs a process-wide check run on every resolved address
// before a connection is made. Pass nil to remove it. Test packages install
// LoopbackOnly so that no test can reach a real network.
func SetDialGuard(f func(address string) error) {
	if f == nil {
		dialGuard.Store(nil)
		return
	}
	dialGuard.Store(&f)
}

// LoopbackOnly is a dial guard that only accepts loopback addresses.
func LoopbackOnly(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("httpx: dial guard: bad address %q: %w", address, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return fmt.Errorf("%w: %q is not a loopback address", errDialBlocked, address)
	}
	return nil
}

// proxyUnlessGuarded is the transport's Proxy func. While a dial guard is
// installed (tests) it never uses a proxy: the guard would only see the
// proxy's address, so a loopback proxy could reach any host. Otherwise it
// follows HTTP_PROXY, HTTPS_PROXY and NO_PROXY.
func proxyUnlessGuarded(req *http.Request) (*url.URL, error) {
	if dialGuard.Load() != nil {
		return nil, nil
	}
	return http.ProxyFromEnvironment(req)
}

// dialControl is used as net.Dialer.Control; address is already resolved.
func dialControl(_, address string, _ syscall.RawConn) error {
	if f := dialGuard.Load(); f != nil {
		return (*f)(address)
	}
	return nil
}
