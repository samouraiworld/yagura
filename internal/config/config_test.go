package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func mapLookup(m map[string]string) LookupFunc {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddress != ":10100" || c.LogLevel != slog.LevelInfo || c.LogFormat != "logfmt" || c.WebConfigFile != "" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_LISTEN_ADDRESS": "127.0.0.1:9", "YAGURA_LOG_LEVEL": "debug", "YAGURA_LOG_FORMAT": "json",
		"YAGURA_WEB_CONFIG_FILE": "/etc/web.yml",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddress != "127.0.0.1:9" || c.LogLevel != slog.LevelDebug || c.LogFormat != "json" || c.WebConfigFile != "/etc/web.yml" {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestLoadInvalidLogLevelAndFormat(t *testing.T) {
	for _, name := range []string{"YAGURA_LOG_LEVEL", "YAGURA_LOG_FORMAT"} {
		val := map[string]string{"YAGURA_LOG_LEVEL": "loud", "YAGURA_LOG_FORMAT": "xml"}[name]
		_, err := Load(mapLookup(map[string]string{name: val}))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: want error naming variable, got %v", name, err)
		}
	}
}

func TestDurationValidation(t *testing.T) {
	const def = time.Minute
	if _, err := duration(mapLookup(map[string]string{"X": "10s"}), "X", def); err == nil || !strings.Contains(err.Error(), "X: must be at least 30s") {
		t.Fatalf("got %v", err)
	}
	if _, err := duration(mapLookup(map[string]string{"X": "abc"}), "X", def); err == nil || !strings.Contains(err.Error(), "X") {
		t.Fatalf("got %v", err)
	}
	if d, err := duration(mapLookup(nil), "X", def); err != nil || d != def {
		t.Fatalf("unset: %v %v", d, err)
	}
	if d, err := duration(mapLookup(map[string]string{"X": "5m"}), "X", def); err != nil || d != 5*time.Minute {
		t.Fatalf("valid: %v %v", d, err)
	}
}

func TestList(t *testing.T) {
	got := list(mapLookup(map[string]string{"X": " a, ,b ,"}), "X", "z")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %q", got)
	}
	if got := list(mapLookup(nil), "X", "p,q"); len(got) != 2 {
		t.Fatalf("default: %q", got)
	}
	if got := list(mapLookup(nil), "X", ""); len(got) != 0 {
		t.Fatalf("empty default: %q", got)
	}
}

func TestBoolean(t *testing.T) {
	if b, err := boolean(mapLookup(map[string]string{"X": "true"}), "X", false); err != nil || !b {
		t.Fatalf("true: %v %v", b, err)
	}
	if b, err := boolean(mapLookup(nil), "X", true); err != nil || !b {
		t.Fatalf("default: %v %v", b, err)
	}
	if _, err := boolean(mapLookup(map[string]string{"X": "maybe"}), "X", false); err == nil || !strings.Contains(err.Error(), "X") {
		t.Fatalf("got %v", err)
	}
}
