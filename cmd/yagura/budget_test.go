package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// freeAddr returns a loopback address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func httpDo(t *testing.T, method, url, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestRunBudgetAPI starts yagura with every provider off, the budget store
// and the API on loopback ports, and checks a change through the API shows
// at the next scrape.
func TestRunBudgetAPI(t *testing.T) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(b)
	dir := t.TempDir()
	tokenFile := filepath.Join(t.TempDir(), "api_token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metricsAddr, apiAddr := freeAddr(t), freeAddr(t)
	env := map[string]string{
		"YAGURA_STATUSPAGE_ENABLED":      "false",
		"YAGURA_LISTEN_ADDRESS":          metricsAddr,
		"YAGURA_STATE_DIR":               dir,
		"YAGURA_BUDGET_INITIAL_TOTAL":    "250",
		"YAGURA_BUDGET_INITIAL_SCALEWAY": "250",
		"YAGURA_API_LISTEN_ADDRESS":      apiAddr,
		"YAGURA_API_TOKEN_FILE":          tokenFile,
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, lookup, logs) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if code, _ := httpDo(t, http.MethodGet, "http://"+metricsAddr+"/healthz", ""); code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics server not up; logs: %s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, metrics := httpDo(t, http.MethodGet, "http://"+metricsAddr+"/metrics", "")
	if !strings.Contains(metrics, `yagura_budget_amount{currency="EUR",project="",scope="total"} 250`) {
		t.Errorf("seeded budget missing from /metrics")
	}
	// The API is not on the metrics listener, and needs the token.
	if code, _ := httpDo(t, http.MethodGet, "http://"+metricsAddr+"/api/v1/budgets", token); code != http.StatusNotFound {
		t.Errorf("API on the metrics listener: %d", code)
	}
	if code, _ := httpDo(t, http.MethodGet, "http://"+apiAddr+"/api/v1/budgets", ""); code != http.StatusUnauthorized {
		t.Errorf("API without token: %d", code)
	}
	if code, body := httpDo(t, http.MethodPost, "http://"+apiAddr+"/api/v1/budgets/set?scope=project&project=acme&amount=120,5", token); code != http.StatusOK {
		t.Fatalf("set: %d %s", code, body)
	}
	_, metrics = httpDo(t, http.MethodGet, "http://"+metricsAddr+"/metrics", "")
	if !strings.Contains(metrics, `yagura_budget_amount{currency="EUR",project="acme",scope="project"} 120.5`) {
		t.Errorf("new budget missing from the next scrape")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not stop")
	}
	if strings.Contains(logs.String(), token) {
		t.Error("logs leak the token")
	}
	for _, s := range []string{"budget store created", "api_listen_address=" + apiAddr, "action=budget_set"} {
		if !strings.Contains(logs.String(), s) {
			t.Errorf("logs miss %q", s)
		}
	}
}

func TestRunBudgetStartupErrors(t *testing.T) {
	dir := t.TempDir()
	busy, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	corrupt := t.TempDir()
	if err := os.WriteFile(filepath.Join(corrupt, "budgets.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("k", 40)
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"missing state dir": {map[string]string{"YAGURA_STATE_DIR": filepath.Join(dir, "absent")}, "budget store"},
		"corrupt store":     {map[string]string{"YAGURA_STATE_DIR": corrupt}, "not valid JSON"},
		"short token": {map[string]string{"YAGURA_STATE_DIR": dir, "YAGURA_API_LISTEN_ADDRESS": freeAddr(t),
			"YAGURA_API_TOKEN": "short"}, "at least 32"},
		"unreadable token file": {map[string]string{"YAGURA_STATE_DIR": dir, "YAGURA_API_LISTEN_ADDRESS": freeAddr(t),
			"YAGURA_API_TOKEN_FILE": filepath.Join(dir, "absent")}, "YAGURA_API_TOKEN_FILE"},
		"busy API port": {map[string]string{"YAGURA_STATE_DIR": dir, "YAGURA_API_LISTEN_ADDRESS": busy.Addr().String(),
			"YAGURA_API_TOKEN": long}, "YAGURA_API_LISTEN_ADDRESS"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			c.env["YAGURA_STATUSPAGE_ENABLED"] = "false"
			c.env["YAGURA_LISTEN_ADDRESS"] = freeAddr(t)
			lookup := func(k string) (string, bool) { v, ok := c.env[k]; return v, ok }
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := run(ctx, lookup, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("run = %v, want an error containing %q", err, c.want)
			}
			if strings.Contains(err.Error(), long) {
				t.Errorf("error leaks the token: %v", err)
			}
		})
	}
}
