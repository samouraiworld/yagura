package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeListener(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "api") })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeListener(ctx, ln, h, slog.New(slog.DiscardHandler)) }()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "api" {
		t.Errorf("body = %q", body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ServeListener = %v, want nil after shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeListener did not return after cancel")
	}
}

func TestServeListenerReturnsServeError(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	if err := ServeListener(context.Background(), ln, http.NotFoundHandler(), slog.New(slog.DiscardHandler)); err == nil {
		t.Error("ServeListener on a closed listener returned nil")
	}
}
