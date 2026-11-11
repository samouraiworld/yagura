package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const defaultStatuspagePages = "supabase=https://status.supabase.com," +
	"scaleway=https://status.scaleway.com," +
	"netlify=https://www.netlifystatus.com," +
	"gandi=https://status.gandi.net"

var pageName = regexp.MustCompile(`^[a-z0-9_]+$`)

// StatuspagePage is one Atlassian Statuspage-compatible page to poll.
type StatuspagePage struct {
	Name    string // used in the poller name and the provider label
	BaseURL string // without trailing slash
}

// StatuspageConfig holds the Statuspage collector settings.
type StatuspageConfig struct {
	Enabled  bool // YAGURA_STATUSPAGE_ENABLED
	Pages    []StatuspagePage
	Interval time.Duration // YAGURA_STATUSPAGE_INTERVAL
}

// loadStatuspage reads the YAGURA_STATUSPAGE_* variables. When the collector
// is disabled, the page and interval variables are not parsed.
func loadStatuspage(lookup LookupFunc) (StatuspageConfig, error) {
	enabled, err := boolean(lookup, "YAGURA_STATUSPAGE_ENABLED", true)
	if err != nil {
		return StatuspageConfig{}, err
	}
	if !enabled {
		return StatuspageConfig{}, nil
	}
	sp := StatuspageConfig{Enabled: true}
	seen := map[string]bool{}
	for _, v := range []struct{ name, def string }{
		{"YAGURA_STATUSPAGE_PAGES", defaultStatuspagePages},
		{"YAGURA_STATUSPAGE_EXTRA_PAGES", ""},
	} {
		for _, entry := range list(lookup, v.name, v.def) {
			page, err := parsePage(entry)
			if err != nil {
				return StatuspageConfig{}, fmt.Errorf("%s: %w", v.name, err)
			}
			if seen[page.Name] {
				return StatuspageConfig{}, fmt.Errorf("%s: duplicate page name %q", v.name, page.Name)
			}
			seen[page.Name] = true
			sp.Pages = append(sp.Pages, page)
		}
	}
	if len(sp.Pages) == 0 {
		return StatuspageConfig{}, fmt.Errorf("YAGURA_STATUSPAGE_PAGES: no page configured; set a page or YAGURA_STATUSPAGE_ENABLED=false")
	}
	if sp.Interval, err = duration(lookup, "YAGURA_STATUSPAGE_INTERVAL", time.Minute); err != nil {
		return StatuspageConfig{}, err
	}
	return sp, nil
}

// parsePage parses one name=baseURL entry.
func parsePage(entry string) (StatuspagePage, error) {
	name, raw, ok := strings.Cut(entry, "=")
	if !ok {
		return StatuspagePage{}, fmt.Errorf("entry %q is not in the form name=url", entry)
	}
	name, raw = strings.TrimSpace(name), strings.TrimSpace(raw)
	if !pageName.MatchString(name) {
		return StatuspagePage{}, fmt.Errorf("page name %q must match ^[a-z0-9_]+$", name)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return StatuspagePage{}, fmt.Errorf("page %q: URL must be http or https with a host", name)
	}
	if u.User != nil {
		return StatuspagePage{}, fmt.Errorf("page %q: URL must not contain credentials", name)
	}
	if u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "?#") {
		return StatuspagePage{}, fmt.Errorf("page %q: URL must not contain a query or fragment", name)
	}
	return StatuspagePage{Name: name, BaseURL: strings.TrimSuffix(raw, "/")}, nil
}
