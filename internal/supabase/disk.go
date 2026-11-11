package supabase

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
)

type diskPoller struct {
	client   *httpx.Client
	interval time.Duration
	logger   *slog.Logger
}

// NewDiskPoller returns the poller of disk utilisation, named "supabase_disk".
func NewDiskPoller(client *httpx.Client, interval time.Duration, logger *slog.Logger) poller.Poller {
	return &diskPoller{client: client, interval: interval, logger: logger}
}

func (p *diskPoller) Name() string            { return "supabase_disk" }
func (p *diskPoller) Interval() time.Duration { return p.interval }

// diskPathSuffix follows the project path to form the disk endpoint.
const diskPathSuffix = "/config/disk/util"

// Poll lists the projects and fetches the disk utilisation of each active one.
func (p *diskPoller) Poll(ctx context.Context) (poller.Result, error) {
	projects, itemErrors, err := listProjects(ctx, p.client, p.logger)
	if err != nil {
		return poller.Result{}, err
	}
	var active []project
	for _, pr := range projects {
		if pr.isActive() {
			active = append(active, pr)
		}
	}
	metrics, n, err := poller.RunItems(ctx, active, p.disk)
	if err != nil {
		return poller.Result{}, err
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors + n}, nil
}

type diskUtil struct {
	Metrics *struct {
		Size  *float64 `json:"fs_size_bytes"`
		Avail *float64 `json:"fs_avail_bytes"`
		Used  *float64 `json:"fs_used_bytes"`
	} `json:"metrics"`
}

// disk fetches the disk utilisation of one project. A missing field omits its
// series only and the item is reported as skipped.
func (p *diskPoller) disk(ctx context.Context, pr project) ([]prometheus.Metric, error) {
	var d diskUtil
	if _, err := p.client.GetJSON(ctx, projectsPath+"/"+pr.Ref+diskPathSuffix, nil, &d); err != nil {
		return nil, fmt.Errorf("supabase disk of %s: %w", pr.Ref, err)
	}
	if d.Metrics == nil {
		return nil, fmt.Errorf("supabase disk of %s: response has no metrics object", pr.Ref)
	}
	var out []prometheus.Metric
	var missing []string
	for _, f := range []struct {
		name string
		v    *float64
		desc *prometheus.Desc
	}{
		{"fs_size_bytes", d.Metrics.Size, diskSizeDesc},
		{"fs_used_bytes", d.Metrics.Used, diskUsedDesc},
		{"fs_avail_bytes", d.Metrics.Avail, diskAvailableDesc},
	} {
		if f.v == nil {
			missing = append(missing, f.name)
			continue
		}
		out = append(out, gauge(f.desc, *f.v, pr.Ref))
	}
	if len(missing) > 0 {
		p.logger.Warn("supabase disk response lacks fields; series omitted",
			"project_ref", pr.Ref, "fields", capped(strings.Join(missing, ",")))
		return out, fmt.Errorf("supabase disk of %s: missing %s: %w", pr.Ref, strings.Join(missing, ","), poller.ErrItemSkipped)
	}
	return out, nil
}
