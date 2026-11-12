package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// supabaseHealthServices lists the services the health endpoint accepts.
var supabaseHealthServices = []string{"auth", "db", "db_postgres_user", "pooler", "realtime", "rest", "storage", "pg_bouncer"}

const defaultSupabaseHealthServices = "auth,db,pooler,realtime,rest,storage"

// SupabaseConfig holds the Supabase collector settings.
type SupabaseConfig struct {
	Token          Secret        // YAGURA_SUPABASE_TOKEN or YAGURA_SUPABASE_TOKEN_FILE
	HealthInterval time.Duration // YAGURA_SUPABASE_HEALTH_INTERVAL (projects and health)
	DiskInterval   time.Duration // YAGURA_SUPABASE_DISK_INTERVAL
	HealthServices []string      // YAGURA_SUPABASE_HEALTH_SERVICES, each once, in the given order
}

// loadSupabase reads the YAGURA_SUPABASE_* variables. It returns nil when no
// token is set: the collector is then disabled and the other variables are
// not parsed.
func loadSupabase(lookup LookupFunc) (*SupabaseConfig, error) {
	token, err := secret(lookup, "YAGURA_SUPABASE_TOKEN")
	if err != nil {
		return nil, err
	}
	if !token.IsSet() {
		return nil, nil
	}
	s := &SupabaseConfig{Token: token}
	if s.HealthServices, err = supabaseServices(lookup); err != nil {
		return nil, err
	}
	if s.HealthInterval, err = duration(lookup, "YAGURA_SUPABASE_HEALTH_INTERVAL", time.Minute); err != nil {
		return nil, err
	}
	if s.DiskInterval, err = duration(lookup, "YAGURA_SUPABASE_DISK_INTERVAL", 5*time.Minute); err != nil {
		return nil, err
	}
	return s, nil
}

// supabaseServices parses YAGURA_SUPABASE_HEALTH_SERVICES. Empty entries are
// skipped and duplicates kept once. Errors identify an unknown entry by its
// position and never echo it: it may be a token set in the wrong variable.
func supabaseServices(lookup LookupFunc) ([]string, error) {
	const name = "YAGURA_SUPABASE_HEALTH_SERVICES"
	raw, ok := get(lookup, name)
	if !ok {
		raw = defaultSupabaseHealthServices
	}
	var out []string
	for i, entry := range strings.Split(raw, ",") {
		// Number by raw comma position; empty entries count but are skipped.
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		if !slices.Contains(supabaseHealthServices, entry) {
			return nil, fmt.Errorf("%s: entry %d: unknown service; allowed: %s", name, i+1, strings.Join(supabaseHealthServices, ", "))
		}
		if !slices.Contains(out, entry) {
			out = append(out, entry)
		}
	}
	if len(out) == 0 {
		return nil, errors.New(name + ": no service configured")
	}
	return out, nil
}
