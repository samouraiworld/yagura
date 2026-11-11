package config

import (
	"strings"
	"testing"
	"time"
)

func TestGandiDisabledWithoutToken(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_GANDI_SHARING_ID":       "00000000-0000-0000-0000-000000000001",
		"YAGURA_GANDI_DOMAINS_INTERVAL": "1s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Gandi != nil {
		t.Fatalf("Gandi enabled without a token: %+v", c.Gandi)
	}
}

func TestGandiDefaults(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{"YAGURA_GANDI_TOKEN": "test-token"}))
	if err != nil {
		t.Fatal(err)
	}
	g := c.Gandi
	if g == nil {
		t.Fatal("Gandi disabled with a token set")
	}
	if g.DomainsInterval != 6*time.Hour || g.BillingInterval != time.Hour || g.SharingID != "" {
		t.Fatalf("unexpected defaults: %+v", g)
	}
	if v, err := g.Token.Read(); err != nil || v != "test-token" {
		t.Fatalf("token: %q %v", v, err)
	}
}

func TestGandiOverrides(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_GANDI_TOKEN":            "test-token",
		"YAGURA_GANDI_SHARING_ID":       "00000000-0000-0000-0000-000000000001",
		"YAGURA_GANDI_DOMAINS_INTERVAL": "12h",
		"YAGURA_GANDI_BILLING_INTERVAL": "30m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	g := c.Gandi
	if g.SharingID != "00000000-0000-0000-0000-000000000001" ||
		g.DomainsInterval != 12*time.Hour || g.BillingInterval != 30*time.Minute {
		t.Fatalf("unexpected config: %+v", g)
	}
}

func TestGandiTokenFile(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{"YAGURA_GANDI_TOKEN_FILE": writeFile(t, "file-token\r\n")}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Gandi == nil {
		t.Fatal("Gandi disabled with a token file set")
	}
	if v, err := c.Gandi.Token.Read(); err != nil || v != "file-token" {
		t.Fatalf("token: %q %v", v, err)
	}
}

func TestGandiValidation(t *testing.T) {
	bad := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"token and file", map[string]string{"YAGURA_GANDI_TOKEN": "s3cretpw", "YAGURA_GANDI_TOKEN_FILE": "/f"}, "YAGURA_GANDI_TOKEN_FILE"},
		{"domains interval", map[string]string{"YAGURA_GANDI_TOKEN": "s3cretpw", "YAGURA_GANDI_DOMAINS_INTERVAL": "10s"}, "YAGURA_GANDI_DOMAINS_INTERVAL"},
		{"billing interval", map[string]string{"YAGURA_GANDI_TOKEN": "s3cretpw", "YAGURA_GANDI_BILLING_INTERVAL": "soon"}, "YAGURA_GANDI_BILLING_INTERVAL"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(mapLookup(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error naming %s, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "s3cretpw") {
				t.Fatalf("error leaks the token: %v", err)
			}
		})
	}
}

func TestGandiSharingIDMustBeUUID(t *testing.T) {
	for _, id := range []string{
		"s3cretpw",
		"00000000-0000-0000-0000-00000000000",
		"00000000-0000-0000-0000-000000000001/s3cretpw",
		"../s3cretpw",
		"00000000-0000-0000-0000-000000000001?s3cretpw",
		"0000000g-0000-0000-0000-000000000001",
		"000000000000000000000000000000000001",
	} {
		t.Run(id, func(t *testing.T) {
			_, err := Load(mapLookup(map[string]string{
				"YAGURA_GANDI_TOKEN":      "test-token",
				"YAGURA_GANDI_SHARING_ID": id,
			}))
			if err == nil || !strings.Contains(err.Error(), "YAGURA_GANDI_SHARING_ID") {
				t.Fatalf("want error naming YAGURA_GANDI_SHARING_ID, got %v", err)
			}
			if strings.Contains(err.Error(), "s3cretpw") || strings.Contains(err.Error(), id) {
				t.Fatalf("error echoes the value: %v", err)
			}
		})
	}
}

func TestGandiSharingIDTrimmed(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_GANDI_TOKEN":      "test-token",
		"YAGURA_GANDI_SHARING_ID": " 0000000A-0000-0000-0000-00000000000b ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Gandi.SharingID != "0000000A-0000-0000-0000-00000000000b" {
		t.Fatalf("got %q", c.Gandi.SharingID)
	}
}
