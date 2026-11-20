// Command yagura is a Prometheus exporter for SaaS status, usage and billing.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/samouraiworld/yagura/internal/api"
	"github.com/samouraiworld/yagura/internal/budget"
	"github.com/samouraiworld/yagura/internal/config"
	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/scaleway"
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

	var catalog *scaleway.Catalog
	var projects func() []scaleway.Project
	if cfg.Scaleway != nil {
		catalog = scaleway.NewCatalog()
		projects = catalog.List
	}
	var store *budget.Store
	if b := cfg.Budget; b != nil {
		store, err = budget.Open(budget.Options{
			Dir:             b.StateDir,
			InitialTotal:    b.InitialTotal,
			InitialScaleway: b.InitialScaleway,
			RequireWritable: cfg.API != nil,
			Logger:          logger,
		})
		if err != nil {
			return err
		}
	}
	var apiListener net.Listener
	if a := cfg.API; a != nil {
		if err := api.CheckToken(a.Token.Read); err != nil {
			return err
		}
		if apiListener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", a.ListenAddress); err != nil {
			return fmt.Errorf("YAGURA_API_LISTEN_ADDRESS: %w", err)
		}
		defer func() { _ = apiListener.Close() }()
	}

	requests := httpx.NewRequestsCounter()
	pollers, err := buildPollers(cfg, deps{logger: logger, requests: requests, catalog: catalog})
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
	if store != nil || projects != nil {
		reg.MustRegister(budget.NewCollector(store, projects))
	}

	attrs := []any{
		"version", version.Version,
		"revision", version.Revision,
		"listen_address", cfg.ListenAddress,
		"collectors", names,
	}
	if cfg.Budget != nil {
		attrs = append(attrs, "state_dir", cfg.Budget.StateDir)
	}
	if apiListener != nil {
		attrs = append(attrs, "api_listen_address", apiListener.Addr().String())
	}
	logger.Info("starting yagura", attrs...)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Run(runCtx)
	}()

	errc := make(chan error, 2)
	servers := 1
	go func() {
		errc <- server.ListenAndServe(runCtx, server.Options{
			ListenAddress: cfg.ListenAddress,
			WebConfigFile: cfg.WebConfigFile,
			Gatherer:      reg,
			Logger:        logger,
		})
	}()
	if apiListener != nil {
		servers++
		h := api.Handler(api.Options{Store: store, Projects: projects, Token: cfg.API.Token.Read, Logger: logger})
		go func() {
			if err := server.ServeListener(runCtx, apiListener, h, logger); err != nil {
				errc <- fmt.Errorf("API server: %w", err)
				return
			}
			errc <- nil
		}()
	}
	// The first server to stop stops the other one and the pollers.
	err = <-errc
	cancel()
	for range servers - 1 {
		if e := <-errc; err == nil {
			err = e
		}
	}
	<-done
	return err
}
