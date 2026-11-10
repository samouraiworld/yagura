// Package server exposes /metrics, /healthz and the landing page.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"
)

const shutdownTimeout = 5 * time.Second

// Options configures the HTTP server.
type Options struct {
	ListenAddress string
	WebConfigFile string
	Gatherer      prometheus.Gatherer
	Logger        *slog.Logger
}

// errorLog adapts slog to promhttp.Logger.
type errorLog struct{ logger *slog.Logger }

func (l errorLog) Println(v ...interface{}) {
	l.logger.Error(fmt.Sprint(v...), "component", "promhttp")
}

// Handler returns the HTTP handler serving /metrics, /healthz and /.
func Handler(opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(opts.Gatherer, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		ErrorLog:      errorLog{opts.Logger},
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	landing, err := web.NewLandingPage(web.LandingConfig{
		Name:        "yagura",
		Description: "Prometheus exporter for SaaS status, usage and billing",
		Links:       []web.LandingLinks{{Address: "/metrics", Text: "Metrics"}},
		// /debug/pprof is not served: no profiling links.
		Profiling: "false",
	})
	if err != nil {
		// The configuration above is static, so this cannot happen.
		panic(err)
	}
	mux.Handle("GET /{$}", landing)
	return mux
}

// newHTTPServer returns the server ListenAndServe runs. The timeouts bound
// how long a slow or idle client can hold a connection.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// ListenAndServe serves until ctx is done, then shuts down gracefully.
// It returns nil on a clean shutdown and the listen error otherwise.
func ListenAndServe(ctx context.Context, opts Options) error {
	srv := newHTTPServer(Handler(opts))
	systemd := false
	flags := &web.FlagConfig{
		WebListenAddresses: &[]string{opts.ListenAddress},
		WebSystemdSocket:   &systemd,
		WebConfigFile:      &opts.WebConfigFile,
	}
	errc := make(chan error, 1)
	go func() { errc <- web.ListenAndServe(srv, flags, opts.Logger) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
