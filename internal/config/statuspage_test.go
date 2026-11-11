package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func spConfig(t *testing.T, env map[string]string) (StatuspageConfig, error) {
	t.Helper()
	c, err := Load(mapLookup(env))
	if err != nil {
		return StatuspageConfig{}, err
	}
	return c.Statuspage, nil
}

func TestStatuspageDefaults(t *testing.T) {
	sp, err := spConfig(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []StatuspagePage{
		{"supabase", "https://status.supabase.com"},
		{"scaleway", "https://status.scaleway.com"},
		{"netlify", "https://www.netlifystatus.com"},
		{"gandi", "https://status.gandi.net"},
	}
	if !sp.Enabled || sp.Interval != time.Minute || !slices.Equal(sp.Pages, want) {
		t.Fatalf("unexpected defaults: %+v", sp)
	}
}

func TestStatuspageReplaceAndExtra(t *testing.T) {
	sp, err := spConfig(t, map[string]string{
		"YAGURA_STATUSPAGE_PAGES":       "a=https://a.example.com",
		"YAGURA_STATUSPAGE_EXTRA_PAGES": "b=https://b.example.com/",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []StatuspagePage{{"a", "https://a.example.com"}, {"b", "https://b.example.com"}}
	if !slices.Equal(sp.Pages, want) {
		t.Fatalf("got %+v", sp.Pages)
	}
}

func TestStatuspageEmptyEntriesAndHTTP(t *testing.T) {
	sp, err := spConfig(t, map[string]string{
		"YAGURA_STATUSPAGE_PAGES": " a = http://a.example.com ,, b=https://b.example.com,",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []StatuspagePage{{"a", "http://a.example.com"}, {"b", "https://b.example.com"}}
	if !slices.Equal(sp.Pages, want) {
		t.Fatalf("got %+v", sp.Pages)
	}
}

func TestStatuspageValidation(t *testing.T) {
	bad := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"bad name", map[string]string{"YAGURA_STATUSPAGE_PAGES": "Bad-Name=https://a.example.com"}, "YAGURA_STATUSPAGE_PAGES"},
		{"duplicate", map[string]string{"YAGURA_STATUSPAGE_PAGES": "a=https://a.example.com,a=https://b.example.com"}, "YAGURA_STATUSPAGE_PAGES"},
		{"duplicate across", map[string]string{
			"YAGURA_STATUSPAGE_PAGES":       "a=https://a.example.com",
			"YAGURA_STATUSPAGE_EXTRA_PAGES": "a=https://b.example.com",
		}, "YAGURA_STATUSPAGE_EXTRA_PAGES"},
		{"ftp", map[string]string{"YAGURA_STATUSPAGE_PAGES": "a=ftp://x.example.com"}, "YAGURA_STATUSPAGE_PAGES"},
		{"no equals", map[string]string{"YAGURA_STATUSPAGE_PAGES": "a"}, "YAGURA_STATUSPAGE_PAGES"},
		{"no host", map[string]string{"YAGURA_STATUSPAGE_PAGES": "a=https://"}, "YAGURA_STATUSPAGE_PAGES"},
		{"query", map[string]string{"YAGURA_STATUSPAGE_PAGES": "a=https://a.example.com/?x=1"}, "YAGURA_STATUSPAGE_PAGES"},
		{"fragment", map[string]string{"YAGURA_STATUSPAGE_PAGES": "a=https://a.example.com/#x"}, "YAGURA_STATUSPAGE_PAGES"},
		{"empty list", map[string]string{"YAGURA_STATUSPAGE_PAGES": " , "}, "YAGURA_STATUSPAGE_PAGES"},
		{"interval too short", map[string]string{"YAGURA_STATUSPAGE_INTERVAL": "10s"}, "YAGURA_STATUSPAGE_INTERVAL"},
		{"bad enabled", map[string]string{"YAGURA_STATUSPAGE_ENABLED": "maybe"}, "YAGURA_STATUSPAGE_ENABLED"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := spConfig(t, tc.env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error naming %s, got %v", tc.want, err)
			}
		})
	}
}

func TestStatuspageDisabledSkipsValidation(t *testing.T) {
	sp, err := spConfig(t, map[string]string{
		"YAGURA_STATUSPAGE_ENABLED":     "false",
		"YAGURA_STATUSPAGE_PAGES":       "Bad-Name=ftp://x",
		"YAGURA_STATUSPAGE_EXTRA_PAGES": "nonsense",
		"YAGURA_STATUSPAGE_INTERVAL":    "1s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sp.Enabled || len(sp.Pages) != 0 {
		t.Fatalf("got %+v", sp)
	}
}
