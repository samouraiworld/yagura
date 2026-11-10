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
	})
	if err != nil {
		// The configuration above is static, so this cannot happen.
		panic(err)
	}
	mux.Handle("GET /{$}", landing)
	return mux
}

// ListenAndServe serves until ctx is done, then shuts down gracefully.
// It returns nil on a clean shutdown and the listen error otherwise.
func ListenAndServe(ctx context.Context, opts Options) error {
	srv := &http.Server{Handler: Handler(opts), ReadHeaderTimeout: 10 * time.Second}
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
