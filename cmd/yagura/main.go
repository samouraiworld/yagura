// Command yagura is a Prometheus exporter for SaaS status, usage and billing.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/samouraiworld/yagura/internal/config"
	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/server"
	"github.com/samouraiworld/yagura/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lookup := func(k string) (string, bool) { return os.LookupEnv(k) }
	if err := run(ctx, lookup, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "yagura:", err)
		stop()
		os.Exit(1)
	}
}

func newLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func run(ctx context.Context, lookup config.LookupFunc, stderr io.Writer) error {
	cfg, err := config.Load(lookup)
	if err != nil {
		return err
	}
	logger := newLogger(cfg, stderr)

	requests := httpx.NewRequestsCounter()
	pollers, err := buildPollers(cfg, deps{logger: logger, requests: requests})
	if err != nil {
		return err
	}
	names := make([]string, 0, len(pollers))
	for _, p := range pollers {
		names = append(names, p.Name())
	}
	scheduler := poller.NewScheduler(logger, nil, pollers...)

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		version.NewCollector(),
		requests,
		scheduler,
	)

	logger.Info("starting yagura",
		"version", version.Version,
		"revision", version.Revision,
		"listen_address", cfg.ListenAddress,
		"collectors", names,
	)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Run(runCtx)
	}()

	err = server.ListenAndServe(runCtx, server.Options{
		ListenAddress: cfg.ListenAddress,
		WebConfigFile: cfg.WebConfigFile,
		Gatherer:      reg,
		Logger:        logger,
	})
	cancel()
	<-done
	return err
}
