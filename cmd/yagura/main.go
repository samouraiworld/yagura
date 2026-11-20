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
	// The token and the API port are checked before the store is opened, so
	// that a bad token or a busy API port does not create budgets.json with
	// the seeds.
	var apiListener net.Listener
	if a := cfg.API; a != nil {
		if err := api.CheckToken(a.Token.Read); err != nil {
			return err
		}
		if apiListener, err = (&net.ListenConfig{}).Listen(context.Background(), "tcp", a.ListenAddress); err != nil {
			return fmt.Errorf("YAGURA_API_LISTEN_ADDRESS: %w", err)
		}
		defer func() { _ = apiListener.Close() }()
	}
	// The history is opened before the budget store, so that a corrupt
	// history file does not create budgets.json with the seeds.
	history, err := openHistory(cfg, logger)
	if err != nil {
		return err
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

	requests := httpx.NewRequestsCounter()
	pollers, err := buildPollers(cfg, deps{logger: logger, requests: requests, catalog: catalog, history: history})
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
	if history != nil {
		reg.MustRegister(scaleway.NewHistoryCollector(history, projectMapper(store)))
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
	if history != nil {
		attrs = append(attrs, "scaleway_history_months", cfg.Scaleway.HistoryMonths,
			"scaleway_history_interval", cfg.Scaleway.HistoryInterval.String())
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
		opts := api.Options{Store: store, Projects: projects, Token: cfg.API.Token.Read, Logger: logger}
		if history != nil {
			opts.History = history.Projects
		}
		h := api.Handler(opts)
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

// openHistory opens the Scaleway monthly history when Scaleway is on and
// YAGURA_SCALEWAY_HISTORY_MONTHS is not 0. Without YAGURA_STATE_DIR it is
// kept in memory only.
func openHistory(cfg *config.Config, logger *slog.Logger) (*scaleway.History, error) {
	s := cfg.Scaleway
	if s == nil || s.HistoryMonths == 0 {
		return nil, nil
	}
	dir := ""
	if cfg.Budget != nil {
		dir = cfg.Budget.StateDir
	}
	history, err := scaleway.OpenHistory(scaleway.HistoryOptions{
		Dir:      dir,
		Months:   s.HistoryMonths,
		Interval: s.HistoryInterval,
		Logger:   logger.With("provider", "scaleway"),
	})
	if err != nil {
		return nil, err
	}
	if dir == "" {
		logger.Warn("YAGURA_STATE_DIR is not set: the Scaleway monthly history is kept in memory only and read again in full after each restart")
	}
	return history, nil
}

// projectMapper maps history projects to business projects with the same
// rule as yagura_scaleway_project_info: the store's manual mapping, else the
// automatic rule. The store may be nil.
func projectMapper(store *budget.Store) scaleway.ProjectMapper {
	return func(projects []scaleway.Project) map[string]string {
		var mappings []budget.Mapping
		if store != nil {
			mappings = store.Mappings()
		}
		out := make(map[string]string, len(projects))
		for _, r := range budget.Resolve(projects, mappings) {
			if r.InBilling {
				out[r.ProjectID] = r.Project
			}
		}
		return out
	}
}
