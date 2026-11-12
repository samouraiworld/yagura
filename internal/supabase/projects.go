// Package supabase polls the Supabase Management API for project status, service health and disk utilisation.
package supabase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
)

const (
	projectsPath    = "/v1/projects"
	healthTimeoutMS = "5000"
	healthyStatus   = "ACTIVE_HEALTHY"
)

// ProjectStatuses lists the values of the project status enum of the API.
var ProjectStatuses = []string{
	"INACTIVE", "ACTIVE_HEALTHY", "ACTIVE_UNHEALTHY", "COMING_UP", "UNKNOWN",
	"GOING_DOWN", "INIT_FAILED", "REMOVED", "RESTORING", "UPGRADING", "PAUSING",
	"RESTORE_FAILED", "RESTARTING", "PAUSE_FAILED", "RESIZING", "PROMOTING",
}

type project struct {
	Ref              string `json:"ref"`
	Name             string `json:"name"`
	OrganizationSlug string `json:"organization_slug"`
	Region           string `json:"region"`
	Status           string `json:"status"`
}

// isActive reports whether the project runs, so that its health and disk can
// be queried.
func (p project) isActive() bool {
	return p.Status == "ACTIVE_HEALTHY" || p.Status == "ACTIVE_UNHEALTHY"
}

// activeProjects returns the projects that run.
func activeProjects(ps []project) []project {
	var out []project
	for _, pr := range ps {
		if pr.isActive() {
			out = append(out, pr)
		}
	}
	return out
}

// logItemError logs the failure of one per-project call: a gone project at
// debug level, other failures as warnings. Auth errors, skipped items (which
// log their own warning) and errors after cancellation are not logged.
func logItemError(ctx context.Context, logger *slog.Logger, what string, ref string, err error) {
	switch {
	case httpx.IsNotFound(err):
		logger.Debug("supabase project gone; "+what+" omitted", "project_ref", ref)
	case httpx.IsAuthError(err), errors.Is(err, poller.ErrItemSkipped), ctx.Err() != nil:
	default:
		logger.Warn("supabase "+what+" call failed", "project_ref", ref, "error", cappedError(err))
	}
}

// validRef reports whether ref is safe to put in a URL path.
func validRef(ref string) bool {
	if ref == "" || len(ref) > 64 {
		return false
	}
	for _, r := range ref {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// listProjects returns the projects, one per ref (the last occurrence wins, in
// first-seen order), and the number of skipped entries (not an object, invalid
// ref, empty status), which the caller adds to its item errors.
func listProjects(ctx context.Context, c *httpx.Client, logger *slog.Logger) ([]project, int, error) {
	var raw []json.RawMessage
	if _, err := c.GetJSON(ctx, projectsPath, nil, &raw); err != nil {
		return nil, 0, fmt.Errorf("supabase projects: %w", err)
	}
	if raw == nil {
		return nil, 0, errors.New("supabase projects: response is not an array")
	}
	var skipped int
	var order []string
	byRef := map[string]project{}
	for _, item := range raw {
		var p project
		if err := json.Unmarshal(item, &p); err != nil {
			logger.Warn("supabase project entry is not a project object; skipped", "error", capped(err.Error()))
			skipped++
			continue
		}
		if !validRef(p.Ref) {
			logger.Warn("supabase project entry has a missing or invalid ref; skipped", "ref", capped(p.Ref))
			skipped++
			continue
		}
		if p.Status == "" {
			logger.Warn("supabase project has no status; skipped", "project_ref", p.Ref)
			skipped++
			continue
		}
		if _, seen := byRef[p.Ref]; !seen {
			order = append(order, p.Ref)
		}
		byRef[p.Ref] = p
	}
	out := make([]project, 0, len(order))
	for _, ref := range order {
		out = append(out, byRef[ref])
	}
	return out, skipped, nil
}

type projectsPoller struct {
	client   *httpx.Client
	services []string
	interval time.Duration
	logger   *slog.Logger
}

// NewProjectsPoller returns the poller of project status and service health,
// named "supabase_projects". Empty and duplicate service names are dropped.
func NewProjectsPoller(client *httpx.Client, services []string, interval time.Duration, logger *slog.Logger) poller.Poller {
	var clean []string
	for _, s := range services {
		if s != "" && !slices.Contains(clean, s) {
			clean = append(clean, s)
		}
	}
	return &projectsPoller{client: client, services: clean, interval: interval, logger: logger}
}

func (p *projectsPoller) Name() string            { return "supabase_projects" }
func (p *projectsPoller) Interval() time.Duration { return p.interval }

// Poll lists the projects, emits their info and status series, then checks the
// health of each active project.
func (p *projectsPoller) Poll(ctx context.Context) (poller.Result, error) {
	projects, itemErrors, err := listProjects(ctx, p.client, p.logger)
	if err != nil {
		return poller.Result{}, err
	}

	var metrics []prometheus.Metric
	for _, pr := range projects {
		metrics = append(metrics, gauge(infoDesc, 1, pr.Ref, pr.Name, pr.OrganizationSlug, pr.Region))
		known := slices.Contains(ProjectStatuses, pr.Status)
		for _, s := range ProjectStatuses {
			v := 0.0
			if s == pr.Status {
				v = 1
			}
			metrics = append(metrics, gauge(statusDesc, v, pr.Ref, s))
		}
		if !known {
			p.logger.Warn("supabase project has an unknown status",
				"project_ref", pr.Ref, "status", capped(pr.Status))
			metrics = append(metrics, gauge(statusDesc, 1, pr.Ref, pr.Status))
			itemErrors++
		}
	}

	health, n, err := poller.RunItems(ctx, activeProjects(projects), p.health)
	metrics = append(metrics, health...)
	itemErrors += n
	if err != nil {
		return poller.Result{}, err
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors}, nil
}

type serviceHealth struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// health fetches the service health of one project. When the call fails it
// reports every requested service as unhealthy and still returns the error,
// so the failure is counted; a missing project (404) emits nothing.
func (p *projectsPoller) health(ctx context.Context, pr project) ([]prometheus.Metric, error) {
	q := url.Values{}
	q.Set("services", strings.Join(p.services, ","))
	q.Set("timeout_ms", healthTimeoutMS)
	var entries []serviceHealth
	_, err := p.client.GetJSON(ctx, projectsPath+"/"+pr.Ref+"/health", q, &entries)
	if err == nil && entries == nil {
		err = errors.New("response is not an array")
	}
	if err != nil {
		err = fmt.Errorf("supabase health of %s: %w", pr.Ref, err)
		logItemError(ctx, p.logger, "health", pr.Ref, err)
		if httpx.IsAuthError(err) || httpx.IsNotFound(err) {
			return nil, err
		}
		out := make([]prometheus.Metric, 0, len(p.services))
		for _, s := range p.services {
			out = append(out, gauge(serviceHealthyDesc, 0, pr.Ref, s))
		}
		return out, err
	}

	// A service listed twice is healthy only if every entry says so.
	healthy := map[string]bool{}
	for _, e := range entries {
		ok := e.Status == healthyStatus
		if prev, seen := healthy[e.Name]; seen {
			ok = ok && prev
		}
		healthy[e.Name] = ok
	}
	out := make([]prometheus.Metric, 0, len(p.services))
	for _, s := range p.services {
		v := 0.0
		if healthy[s] {
			v = 1
		}
		out = append(out, gauge(serviceHealthyDesc, v, pr.Ref, s))
	}
	return out, nil
}

// maxLogValue caps remote strings written to the logs.
const maxLogValue = 64

// maxLogError caps error text written to the logs. It is larger than
// maxLogValue so the cause survives: httpx errors carry only the method, path
// and status (never query or headers) and the refs in the path are validated.
const maxLogError = 256

// cappedError returns at most maxLogError bytes of err's text, marking a cut.
func cappedError(err error) string {
	s := err.Error()
	if len(s) <= maxLogError {
		return s
	}
	return s[:maxLogError] + "...(truncated)"
}

// capped returns at most maxLogValue bytes of s, marking a cut.
func capped(s string) string {
	if len(s) <= maxLogValue {
		return s
	}
	return s[:maxLogValue] + "...(truncated)"
}
