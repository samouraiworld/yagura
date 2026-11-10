// Package httpx is the only way yagura talks to a provider API: GET-only,
// size-limited, with errors that never carry query strings or headers.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"

	"github.com/samourai-crew/yagura/internal/version"
)

const maxBodyBytes = 10 << 20

// MaxPages bounds paginated listings; a var so tests can lower it.
var MaxPages = 1000

var (
	// ErrDecode wraps JSON decoding failures.
	ErrDecode = errors.New("httpx: cannot decode JSON response")
	// ErrBodyTooLarge is returned when a response exceeds the 10 MiB limit.
	ErrBodyTooLarge = errors.New("httpx: response body too large")
)

// Options configures a Client.
type Options struct {
	Provider   string                    // label of yagura_api_requests_total
	BaseURL    string                    // e.g. "https://api.example.com"
	Auth       func(*http.Request) error // nil = no auth
	RateLimit  rate.Limit                // 0 = unlimited (tests)
	Burst      int
	ResetDelay func(h http.Header, now time.Time) (time.Duration, bool) // optional, used by retries
	Requests   *prometheus.CounterVec                                   // may be nil
}

// Client performs GET requests against one provider.
type Client struct {
	opts    Options
	base    *url.URL
	http    *http.Client
	limiter *rate.Limiter
}

// New builds a Client on the dial-guarded production transport.
func New(opts Options) *Client {
	return newClient(opts, &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: dialControl,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	})
}

// newClient is the test seam: base is the transport underneath the GET guard.
func newClient(opts Options, base http.RoundTripper) *Client {
	u, err := url.Parse(opts.BaseURL)
	if err != nil {
		// Fail every request later rather than panic at startup; the message
		// carries no URL because base URLs come from trusted code, not config.
		u = &url.URL{}
	}
	limit := opts.RateLimit
	if limit == 0 {
		limit = rate.Inf
	}
	burst := max(opts.Burst, 1)
	return &Client{
		opts: opts,
		base: u,
		http: &http.Client{
			Transport: &getOnlyTransport{next: base},
			Timeout:   time.Minute,
			// Never follow redirects: they could carry credentials elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		limiter: rate.NewLimiter(limit, burst),
	}
}

// NewRequestsCounter returns yagura_api_requests_total{provider,code}.
func NewRequestsCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "yagura_api_requests_total",
		Help: "Outbound API requests by provider and HTTP status code (\"error\" when no response was received).",
	}, []string{"provider", "code"})
}

// BearerAuth sets "Authorization: Bearer <value>" using the secret reader.
func BearerAuth(read func() (string, error)) func(*http.Request) error {
	return func(r *http.Request) error {
		v, err := read()
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+v)
		return nil
	}
}

// HeaderAuth sets the named header to the secret value.
func HeaderAuth(name string, read func() (string, error)) func(*http.Request) error {
	return func(r *http.Request) error {
		v, err := read()
		if err != nil {
			return err
		}
		r.Header.Set(name, v)
		return nil
	}
}

// StatusError reports a non-2xx response. It never holds a query or headers.
type StatusError struct {
	Path string
	Code int
}

func (e *StatusError) Error() string { return "GET " + e.Path + ": HTTP " + strconv.Itoa(e.Code) }

// IsAuthError reports whether err is an HTTP 401 or 403.
func IsAuthError(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden)
}

// IsNotFound reports whether err is an HTTP 404.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusNotFound
}

// GetJSON performs one GET and decodes the JSON body into out. It returns the
// response headers. Retries and rate limiting are layered on in a later task.
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, out any) (http.Header, error) {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: cannot build request", path)
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")
	if c.opts.Auth != nil {
		if err := c.opts.Auth(req); err != nil {
			return nil, fmt.Errorf("GET %s: auth: %w", path, err)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.count("error")
		// net/http wraps errors in *url.Error whose text holds the full URL,
		// query string included. Keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.count(strconv.Itoa(resp.StatusCode))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return resp.Header, &StatusError{Path: path, Code: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return resp.Header, fmt.Errorf("GET %s: reading body: %w", path, err)
	}
	if len(body) > maxBodyBytes {
		return resp.Header, fmt.Errorf("GET %s: %w", path, ErrBodyTooLarge)
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(out); err != nil {
		return resp.Header, fmt.Errorf("GET %s: %w: %v", path, ErrDecode, err)
	}
	return resp.Header, nil
}

func (c *Client) count(code string) {
	if c.opts.Requests != nil {
		c.opts.Requests.WithLabelValues(c.opts.Provider, code).Inc()
	}
}
