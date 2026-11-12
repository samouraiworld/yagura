package config

import (
	"strings"
	"testing"
	"time"
)

func TestNetlifyDisabledWithoutToken(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_NETLIFY_DEPLOYS_INTERVAL": "1s",
		"YAGURA_NETLIFY_BUILDS_INTERVAL":  "soon",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Netlify != nil {
		t.Fatalf("Netlify enabled without a token: %+v", c.Netlify)
	}
}

func TestNetlifyDefaults(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{"YAGURA_NETLIFY_TOKEN": "test-token"}))
	if err != nil {
		t.Fatal(err)
	}
	n := c.Netlify
	if n == nil {
		t.Fatal("Netlify disabled with a token set")
	}
	if n.DeploysInterval != 5*time.Minute || n.BuildsInterval != 15*time.Minute {
		t.Fatalf("unexpected intervals: %+v", n)
	}
	if v, err := n.Token.Read(); err != nil || v != "test-token" {
		t.Fatalf("token: %q %v", v, err)
	}
}

func TestNetlifyOverrides(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_NETLIFY_TOKEN":            "test-token",
		"YAGURA_NETLIFY_DEPLOYS_INTERVAL": "2m",
		"YAGURA_NETLIFY_BUILDS_INTERVAL":  "1h",
	}))
	if err != nil {
		t.Fatal(err)
	}
	n := c.Netlify
	if n.DeploysInterval != 2*time.Minute || n.BuildsInterval != time.Hour {
		t.Fatalf("unexpected intervals: %+v", n)
	}
}

func TestNetlifyTokenFile(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_NETLIFY_TOKEN_FILE": writeFile(t, "file-token\r\n"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Netlify == nil {
		t.Fatal("Netlify disabled with a token file set")
	}
	if v, err := c.Netlify.Token.Read(); err != nil || v != "file-token" {
		t.Fatalf("token: %q %v", v, err)
	}
}

func TestNetlifyValidation(t *testing.T) {
	bad := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"token and file", map[string]string{ //nolint:gosec // fake credential and path, asserts neither is echoed
			"YAGURA_NETLIFY_TOKEN":      "s3cretpw",
			"YAGURA_NETLIFY_TOKEN_FILE": "/run/demo-file-1",
		}, "YAGURA_NETLIFY_TOKEN_FILE"},
		{"whitespace-only token", map[string]string{
			"YAGURA_NETLIFY_TOKEN": " \t\r\n",
		}, "YAGURA_NETLIFY_TOKEN: secret is empty"},
		{"blank token file path", map[string]string{
			"YAGURA_NETLIFY_TOKEN_FILE": " \n",
		}, "YAGURA_NETLIFY_TOKEN_FILE: path is empty"},
		{"deploys interval too short", map[string]string{
			"YAGURA_NETLIFY_TOKEN":            "s3cretpw",
			"YAGURA_NETLIFY_DEPLOYS_INTERVAL": "10s",
		}, "YAGURA_NETLIFY_DEPLOYS_INTERVAL"},
		{"deploys interval unparseable", map[string]string{
			"YAGURA_NETLIFY_TOKEN":            "s3cretpw",
			"YAGURA_NETLIFY_DEPLOYS_INTERVAL": "soon",
		}, "YAGURA_NETLIFY_DEPLOYS_INTERVAL"},
		{"builds interval too short", map[string]string{
			"YAGURA_NETLIFY_TOKEN":           "s3cretpw",
			"YAGURA_NETLIFY_BUILDS_INTERVAL": "10s",
		}, "YAGURA_NETLIFY_BUILDS_INTERVAL"},
		{"builds interval unparseable", map[string]string{
			"YAGURA_NETLIFY_TOKEN":           "s3cretpw",
			"YAGURA_NETLIFY_BUILDS_INTERVAL": "soon",
		}, "YAGURA_NETLIFY_BUILDS_INTERVAL"},
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
