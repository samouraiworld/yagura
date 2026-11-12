package main

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/config"
	"github.com/samouraiworld/yagura/internal/gandi"
	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
	"github.com/samouraiworld/yagura/internal/scaleway"
	"github.com/samouraiworld/yagura/internal/statuspage"
	"github.com/samouraiworld/yagura/internal/supabase"
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
	if g := cfg.Gandi; g != nil {
		client := httpx.New(httpx.Options{
			Provider:  "gandi",
			BaseURL:   "https://api.gandi.net",
			Auth:      httpx.BearerAuth(g.Token.Read),
			RateLimit: 5,
			Burst:     5,
			Requests:  d.requests,
		})
		logger := d.logger.With("provider", "gandi")
		pollers = append(pollers,
			gandi.NewDomainsPoller(client, g.SharingID, g.DomainsInterval, logger),
			gandi.NewBillingPoller(client, g.SharingID, g.BillingInterval, logger),
		)
	}
	if s := cfg.Scaleway; s != nil {
		client := httpx.New(httpx.Options{
			Provider:  "scaleway",
			BaseURL:   "https://api.scaleway.com",
			Auth:      httpx.HeaderAuth("X-Auth-Token", s.SecretKey.Read),
			RateLimit: 1,
			Burst:     2,
			Requests:  d.requests,
		})
		logger := d.logger.With("provider", "scaleway")
		pollers = append(pollers, scaleway.NewConsumptionPoller(client, s.OrganizationID, s.Interval, logger))
	}
	if s := cfg.Supabase; s != nil {
		client := httpx.New(httpx.Options{
			Provider:   "supabase",
			BaseURL:    "https://api.supabase.com",
			Auth:       httpx.BearerAuth(s.Token.Read),
			RateLimit:  10,
			Burst:      10,
			ResetDelay: httpx.ResetSeconds,
			Requests:   d.requests,
		})
		logger := d.logger.With("provider", "supabase")
		pollers = append(pollers,
			supabase.NewProjectsPoller(client, s.HealthServices, s.HealthInterval, logger),
			supabase.NewDiskPoller(client, s.DiskInterval, logger),
		)
	}
	return pollers, nil
}
