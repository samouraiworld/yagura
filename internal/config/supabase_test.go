package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSupabaseDisabledWithoutToken(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_SUPABASE_HEALTH_SERVICES": "unknown",
		"YAGURA_SUPABASE_HEALTH_INTERVAL": "1s",
		"YAGURA_SUPABASE_DISK_INTERVAL":   "soon",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Supabase != nil {
		t.Fatalf("Supabase enabled without a token: %+v", c.Supabase)
	}
}

func TestSupabaseDefaults(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{"YAGURA_SUPABASE_TOKEN": "test-token"}))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Supabase
	if s == nil {
		t.Fatal("Supabase disabled with a token set")
	}
	if s.HealthInterval != time.Minute || s.DiskInterval != 5*time.Minute {
		t.Fatalf("unexpected intervals: %+v", s)
	}
	want := []string{"auth", "db", "pooler", "realtime", "rest", "storage"}
	if !slices.Equal(s.HealthServices, want) {
		t.Fatalf("services = %v, want %v", s.HealthServices, want)
	}
	if v, err := s.Token.Read(); err != nil || v != "test-token" {
		t.Fatalf("token: %q %v", v, err)
	}
}

func TestSupabaseOverrides(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_SUPABASE_TOKEN":           "test-token",
		"YAGURA_SUPABASE_HEALTH_INTERVAL": "2m",
		"YAGURA_SUPABASE_DISK_INTERVAL":   "15m",
		"YAGURA_SUPABASE_HEALTH_SERVICES": " pg_bouncer, db_postgres_user ,,db,pg_bouncer, auth,pooler,realtime,rest,storage",
	}))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Supabase
	if s.HealthInterval != 2*time.Minute || s.DiskInterval != 15*time.Minute {
		t.Fatalf("unexpected intervals: %+v", s)
	}
	// Trimmed, empties dropped, duplicates kept once, order kept.
	want := []string{"pg_bouncer", "db_postgres_user", "db", "auth", "pooler", "realtime", "rest", "storage"}
	if !slices.Equal(s.HealthServices, want) {
		t.Fatalf("services = %v, want %v", s.HealthServices, want)
	}
}

func TestSupabaseTokenFile(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_SUPABASE_TOKEN_FILE": writeFile(t, "file-token\n"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Supabase == nil {
		t.Fatal("Supabase disabled with a token file set")
	}
	if v, err := c.Supabase.Token.Read(); err != nil || v != "file-token" {
		t.Fatalf("token: %q %v", v, err)
	}
}

func TestSupabaseUnknownService(t *testing.T) {
	for _, services := range []string{
		"auth,s3cretpw",
		"s3cretpw",
		"Auth",
		"auth db",
		"auth;db",
	} {
		t.Run(services, func(t *testing.T) {
			_, err := Load(mapLookup(map[string]string{
				"YAGURA_SUPABASE_TOKEN":           "test-token",
				"YAGURA_SUPABASE_HEALTH_SERVICES": services,
			}))
			if err == nil || !strings.Contains(err.Error(), "YAGURA_SUPABASE_HEALTH_SERVICES") {
				t.Fatalf("want error naming YAGURA_SUPABASE_HEALTH_SERVICES, got %v", err)
			}
			if strings.Contains(err.Error(), "s3cretpw") || strings.Contains(err.Error(), services) {
				t.Fatalf("error echoes the value: %v", err)
			}
		})
	}
}

func TestSupabaseUnknownServiceNamesEntry(t *testing.T) {
	_, err := Load(mapLookup(map[string]string{
		"YAGURA_SUPABASE_TOKEN":           "test-token",
		"YAGURA_SUPABASE_HEALTH_SERVICES": "auth,,unknown",
	}))
	// Entries are numbered by raw comma position, like Statuspage pages.
	if err == nil || !strings.Contains(err.Error(), "entry 3") {
		t.Fatalf("want error naming entry 3, got %v", err)
	}
	for _, s := range []string{"auth", "db", "db_postgres_user", "pooler", "realtime", "rest", "storage", "pg_bouncer"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error does not list the allowed service %q: %v", s, err)
		}
	}
}

func TestSupabaseValidation(t *testing.T) {
	bad := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"token and file", map[string]string{ //nolint:gosec // fake credential and path, asserts neither is echoed
			"YAGURA_SUPABASE_TOKEN":      "s3cretpw",
			"YAGURA_SUPABASE_TOKEN_FILE": "/run/demo-file-1",
		}, "YAGURA_SUPABASE_TOKEN_FILE"},
		{"whitespace-only token", map[string]string{
			"YAGURA_SUPABASE_TOKEN": " \t\r\n",
		}, "YAGURA_SUPABASE_TOKEN: secret is empty"},
		{"blank token file path", map[string]string{
			"YAGURA_SUPABASE_TOKEN_FILE": " \n",
		}, "YAGURA_SUPABASE_TOKEN_FILE: path is empty"},
		{"no service", map[string]string{
			"YAGURA_SUPABASE_TOKEN":           "s3cretpw",
			"YAGURA_SUPABASE_HEALTH_SERVICES": " , ,",
		}, "YAGURA_SUPABASE_HEALTH_SERVICES"},
		{"health interval too short", map[string]string{
			"YAGURA_SUPABASE_TOKEN":           "s3cretpw",
			"YAGURA_SUPABASE_HEALTH_INTERVAL": "10s",
		}, "YAGURA_SUPABASE_HEALTH_INTERVAL"},
		{"health interval unparseable", map[string]string{
			"YAGURA_SUPABASE_TOKEN":           "s3cretpw",
			"YAGURA_SUPABASE_HEALTH_INTERVAL": "soon",
		}, "YAGURA_SUPABASE_HEALTH_INTERVAL"},
		{"disk interval too short", map[string]string{
			"YAGURA_SUPABASE_TOKEN":         "s3cretpw",
			"YAGURA_SUPABASE_DISK_INTERVAL": "10s",
		}, "YAGURA_SUPABASE_DISK_INTERVAL"},
		{"disk interval unparseable", map[string]string{
			"YAGURA_SUPABASE_TOKEN":         "s3cretpw",
			"YAGURA_SUPABASE_DISK_INTERVAL": "soon",
		}, "YAGURA_SUPABASE_DISK_INTERVAL"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(mapLookup(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error naming %s, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "s3cretpw") || strings.Contains(err.Error(), "demo-file-1") {
				t.Fatalf("error leaks the token or its file path: %v", err)
			}
		})
	}
}
