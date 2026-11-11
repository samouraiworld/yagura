package main

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samourai-crew/yagura/internal/config"
	"github.com/samourai-crew/yagura/internal/httpx"
	"github.com/samourai-crew/yagura/internal/poller"
	"github.com/samourai-crew/yagura/internal/statuspage"
)

// deps holds what provider pollers need to be built.
type deps struct {
	logger   *slog.Logger
	requests *prometheus.CounterVec
}

// buildPollers returns the pollers for the enabled providers.
func buildPollers(cfg *config.Config, d deps) ([]poller.Poller, error) {
	var pollers []poller.Poller
	if cfg.Statuspage.Enabled {
		for _, page := range cfg.Statuspage.Pages {
			client := httpx.New(httpx.Options{
				Provider:  "statuspage",
				BaseURL:   page.BaseURL,
				RateLimit: 1,
				Burst:     3,
				Requests:  d.requests,
			})
			pollers = append(pollers, statuspage.NewPoller(page.Name, client, cfg.Statuspage.Interval, d.logger))
		}
	}
	return pollers, nil
}
