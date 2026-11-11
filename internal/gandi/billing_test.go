package gandi

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/testkit"
)

const (
	billingID = "00000000-0000-0000-0000-000000000001"
)

func newBilling(t *testing.T, sharingID string, routes map[string]testkit.Route, logs *bytes.Buffer) poller.Poller {
	t.Helper()
	srv := testkit.NewServer(t, routes)
	client := httpx.New(httpx.Options{Provider: "gandi", BaseURL: srv.URL})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewBillingPoller(client, sharingID, time.Minute, logger)
}

func TestBillingPrepaid(t *testing.T) {
	p := newBilling(t, "", map[string]testkit.Route{
		billingPath: {File: "testdata/billing_prepaid.json"},
	}, &bytes.Buffer{})
	res, err := poll(t, p)
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, res)
	if got := v["yagura_gandi_prepaid_balance"]["EUR"]; got != 42.5 {
		t.Errorf("balance = %v, want 42.5", got)
	}
	if got := v["yagura_gandi_prepaid_warning_threshold"]["EUR"]; got != 100 {
		t.Errorf("threshold = %v, want 100", got)
	}
	if res.ItemErrors != 0 {
		t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
	}
}

func TestBillingNoPrepaid(t *testing.T) {
	for name, route := range map[string]testkit.Route{
		"absent":    {File: "testdata/billing_no_prepaid.json"},
		"null":      {Body: `{"prepaid":null}`},
		"empty obj": {Body: `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			p := newBilling(t, "", map[string]testkit.Route{billingPath: route}, &bytes.Buffer{})
			res, err := poll(t, p)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Metrics) != 0 || res.ItemErrors != 0 {
				t.Errorf("metrics %d, ItemErrors %d, want 0 and 0", len(res.Metrics), res.ItemErrors)
			}
		})
	}
}

func TestBillingSharingIDPath(t *testing.T) {
	key := billingPath + "/" + billingID
	srv := testkit.NewServer(t, map[string]testkit.Route{key: {File: "testdata/billing_prepaid.json"}})
	client := httpx.New(httpx.Options{Provider: "gandi", BaseURL: srv.URL})
	p := NewBillingPoller(client, billingID, time.Minute, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if _, err := poll(t, p); err != nil {
		t.Fatal(err)
	}
	if got := testkit.Hits(srv, key); got != 1 {
		t.Errorf("requests on %s = %d, want 1", key, got)
	}
}

func TestBillingPartialPrepaid(t *testing.T) {
	cases := []struct {
		name, body string
		series     map[string]float64 // metric name -> expected value for EUR
		itemErrors int
	}{
		{"no currency", `{"prepaid":{"amount":1,"warning_threshold":2}}`, nil, 1},
		{"empty currency", `{"prepaid":{"amount":1,"currency":"","warning_threshold":2}}`, nil, 1},
		{"no amount", `{"prepaid":{"currency":"EUR","warning_threshold":2}}`,
			map[string]float64{"yagura_gandi_prepaid_warning_threshold": 2}, 1},
		{"no threshold", `{"prepaid":{"amount":0,"currency":"EUR"}}`,
			map[string]float64{"yagura_gandi_prepaid_balance": 0}, 0},
		{"null threshold", `{"prepaid":{"amount":1,"currency":"EUR","warning_threshold":null}}`,
			map[string]float64{"yagura_gandi_prepaid_balance": 1}, 0},
		{"string threshold", `{"prepaid":{"amount":1,"currency":"EUR","warning_threshold":"100"}}`,
			map[string]float64{"yagura_gandi_prepaid_balance": 1}, 1},
		{"object threshold", `{"prepaid":{"amount":1,"currency":"EUR","warning_threshold":{}}}`,
			map[string]float64{"yagura_gandi_prepaid_balance": 1}, 1},
		{"null amount", `{"prepaid":{"amount":null,"currency":"EUR","warning_threshold":2}}`,
			map[string]float64{"yagura_gandi_prepaid_warning_threshold": 2}, 1},
		{"no amount nor threshold", `{"prepaid":{"currency":"EUR"}}`, nil, 1},
		{"no amount, string threshold", `{"prepaid":{"currency":"EUR","warning_threshold":"x"}}`, nil, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newBilling(t, "", map[string]testkit.Route{billingPath: {Body: c.body}}, &bytes.Buffer{})
			res, err := poll(t, p)
			if err != nil {
				t.Fatal(err)
			}
			if res.ItemErrors != c.itemErrors {
				t.Errorf("ItemErrors = %d, want %d", res.ItemErrors, c.itemErrors)
			}
			v := values(t, res)
			for _, name := range MetricNames[2:] {
				want, ok := c.series[name]
				got, present := v[name]["EUR"]
				if present != ok || got != want {
					t.Errorf("%s = %v (present %v), want %v (present %v)", name, got, present, want, ok)
				}
			}
		})
	}
}

// An account without a warning threshold is a valid setting: the balance is
// exported, the threshold series is omitted, and nothing is logged as a fault.
func TestBillingPrepaidWithoutThresholdIsQuiet(t *testing.T) {
	for name, body := range map[string]string{
		"absent": `{"prepaid":{"amount":0,"currency":"EUR"}}`,
		"null":   `{"prepaid":{"amount":0,"currency":"EUR","warning_threshold":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			p := newBilling(t, "", map[string]testkit.Route{billingPath: {Body: body}}, &logs)
			res, err := poll(t, p)
			if err != nil {
				t.Fatal(err)
			}
			if res.ItemErrors != 0 {
				t.Errorf("ItemErrors = %d, want 0", res.ItemErrors)
			}
			if strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("threshold not set must not warn:\n%s", logs.String())
			}
		})
	}
}

// A threshold that is not a usable number is logged by field name only: the
// remote value never reaches the logs.
func TestBillingInvalidThresholdValueNotLogged(t *testing.T) {
	for name, raw := range map[string]string{
		"string":   `"demo-value-4242"`,
		"overflow": `1e400`,
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			body := `{"prepaid":{"amount":1,"currency":"EUR","warning_threshold":` + raw + `}}`
			p := newBilling(t, "", map[string]testkit.Route{billingPath: {Body: body}}, &logs)
			res, err := poll(t, p)
			if err != nil {
				t.Fatal(err)
			}
			if res.ItemErrors != 1 {
				t.Errorf("ItemErrors = %d, want 1", res.ItemErrors)
			}
			if !strings.Contains(logs.String(), "warning_threshold") {
				t.Errorf("invalid threshold not logged by field name:\n%s", logs.String())
			}
			if strings.Contains(logs.String(), "4242") || strings.Contains(logs.String(), "1e400") {
				t.Errorf("log leaks the remote value:\n%s", logs.String())
			}
		})
	}
}

func TestBillingFailures(t *testing.T) {
	cases := map[string]testkit.Route{
		"http error":  {Status: 500, Body: `{"message":"boom"}`},
		"forbidden":   {Status: 403, Body: `{}`},
		"not json":    {Body: "<html>"},
		"null":        {Body: "null"},
		"array":       {Body: `[]`},
		"string body": {Body: `"x"`},
		"empty body":  {Body: " "},
		// The balance is required: a wrong type fails the poll so that the
		// last good balance stays exported instead of vanishing.
		"string amount": {Body: `{"prepaid":{"amount":"42.5","currency":"EUR","warning_threshold":100}}`},
	}
	for name, route := range cases {
		t.Run(name, func(t *testing.T) {
			p := newBilling(t, "", map[string]testkit.Route{billingPath: route}, &bytes.Buffer{})
			if _, err := poll(t, p); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	t.Run("decode error kind", func(t *testing.T) {
		p := newBilling(t, "", map[string]testkit.Route{billingPath: {Body: `[]`}}, &bytes.Buffer{})
		if _, err := poll(t, p); !errors.Is(err, httpx.ErrDecode) {
			t.Errorf("err = %v, want ErrDecode", err)
		}
	})
}

func TestBillingName(t *testing.T) {
	p := newBilling(t, "", nil, &bytes.Buffer{})
	if p.Name() != "gandi_billing" || p.Interval() != time.Minute {
		t.Errorf("name %q interval %v", p.Name(), p.Interval())
	}
}
