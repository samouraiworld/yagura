package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/samouraiworld/yagura/internal/config"
	"github.com/samouraiworld/yagura/internal/httpx"
)

func TestRunConfigError(t *testing.T) {
	env := map[string]string{"YAGURA_LOG_LEVEL": "loud"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	err := run(context.Background(), lookup, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "YAGURA_LOG_LEVEL") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildPollersStatuspage(t *testing.T) {
	cfg, err := config.Load(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	ps, err := buildPollers(cfg, deps{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), requests: httpx.NewRequestsCounter()})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.Name())
	}
	want := []string{"statuspage_supabase", "statuspage_scaleway", "statuspage_netlify", "statuspage_gandi"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	cfg.Statuspage.Enabled = false
	ps, err = buildPollers(cfg, deps{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), requests: httpx.NewRequestsCounter()})
	if err != nil || len(ps) != 0 {
		t.Fatalf("disabled: %v %v", ps, err)
	}
}

func TestBuildPollersGandi(t *testing.T) {
	cfg, err := config.Load(func(k string) (string, bool) {
		switch k {
		case "YAGURA_GANDI_TOKEN":
			return "test-token", true
		case "YAGURA_STATUSPAGE_ENABLED":
			return "false", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := buildPollers(cfg, deps{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), requests: httpx.NewRequestsCounter()})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.Name())
	}
	want := []string{"gandi_domains", "gandi_billing"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// attrRecorder is a slog.Handler that records the attributes bound with
// Logger.With, so a test can see what a poller's logger carries.
type attrRecorder struct {
	slog.Handler
	attrs *[]slog.Attr
}

func (h attrRecorder) WithAttrs(as []slog.Attr) slog.Handler {
	*h.attrs = append(*h.attrs, as...)
	return h
}

func TestBuildPollersGandiLoggerProvider(t *testing.T) {
	cfg, err := config.Load(func(k string) (string, bool) {
		switch k {
		case "YAGURA_GANDI_TOKEN":
			return "test-token", true
		case "YAGURA_STATUSPAGE_ENABLED":
			return "false", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	var attrs []slog.Attr
	logger := slog.New(attrRecorder{Handler: slog.NewTextHandler(io.Discard, nil), attrs: &attrs})
	if _, err := buildPollers(cfg, deps{logger: logger, requests: httpx.NewRequestsCounter()}); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(attrs, func(a slog.Attr) bool { return a.Equal(slog.String("provider", "gandi")) }) {
		t.Fatalf("gandi logger attributes = %v, want provider=gandi", attrs)
	}
}

// scalewayLookup enables only the Scaleway collector.
func scalewayLookup(k string) (string, bool) {
	switch k {
	case "YAGURA_SCALEWAY_SECRET_KEY":
		return "test-key", true
	case "YAGURA_SCALEWAY_ORGANIZATION_ID":
		return "00000000-0000-0000-0000-000000000001", true
	case "YAGURA_STATUSPAGE_ENABLED":
		return "false", true
	}
	return "", false
}

func TestBuildPollersScaleway(t *testing.T) {
	cfg, err := config.Load(scalewayLookup)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := buildPollers(cfg, deps{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), requests: httpx.NewRequestsCounter()})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.Name())
	}
	want := []string{"scaleway_consumption"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildPollersScalewayLoggerProvider(t *testing.T) {
	cfg, err := config.Load(scalewayLookup)
	if err != nil {
		t.Fatal(err)
	}
	var attrs []slog.Attr
	logger := slog.New(attrRecorder{Handler: slog.NewTextHandler(io.Discard, nil), attrs: &attrs})
	if _, err := buildPollers(cfg, deps{logger: logger, requests: httpx.NewRequestsCounter()}); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(attrs, func(a slog.Attr) bool { return a.Equal(slog.String("provider", "scaleway")) }) {
		t.Fatalf("scaleway logger attributes = %v, want provider=scaleway", attrs)
	}
}

func TestMain(m *testing.M) {
	httpx.SetDialGuard(httpx.LoopbackOnly)
	os.Exit(m.Run())
}
