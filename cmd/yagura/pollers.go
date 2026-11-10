package main

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samourai-crew/yagura/internal/config"
	"github.com/samourai-crew/yagura/internal/poller"
)

// deps holds what provider pollers need to be built.
type deps struct {
	logger   *slog.Logger
	requests *prometheus.CounterVec
}

// buildPollers returns the pollers for the enabled providers.
// Provider pollers are added in later tasks; none exist yet.
func buildPollers(_ *config.Config, _ deps) ([]poller.Poller, error) {
	return nil, nil
}
