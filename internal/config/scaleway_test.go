package config

import (
	"strings"
	"testing"
	"time"
)

const testOrganizationID = "00000000-0000-0000-0000-000000000001"

func TestScalewayDisabledWithoutKey(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_SCALEWAY_ORGANIZATION_ID": "not-a-uuid",
		"YAGURA_SCALEWAY_INTERVAL":        "1s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Scaleway != nil {
		t.Fatalf("Scaleway enabled without a secret key: %+v", c.Scaleway)
	}
}

func TestScalewayRequiresOrganization(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"unset": {"YAGURA_SCALEWAY_SECRET_KEY": "s3cretpw"},
		"blank": {"YAGURA_SCALEWAY_SECRET_KEY": "s3cretpw", "YAGURA_SCALEWAY_ORGANIZATION_ID": "  "},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(mapLookup(env))
			if err == nil || !strings.Contains(err.Error(), "YAGURA_SCALEWAY_ORGANIZATION_ID") {
				t.Fatalf("want error naming YAGURA_SCALEWAY_ORGANIZATION_ID, got %v", err)
			}
			if strings.Contains(err.Error(), "s3cretpw") {
				t.Fatalf("error leaks the key: %v", err)
			}
		})
	}
}

func TestScalewayDefaults(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_SCALEWAY_SECRET_KEY":      "test-key",
		"YAGURA_SCALEWAY_ORGANIZATION_ID": testOrganizationID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Scaleway
	if s == nil {
		t.Fatal("Scaleway disabled with a secret key set")
	}
	if s.Interval != time.Hour || s.OrganizationID != testOrganizationID {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if v, err := s.SecretKey.Read(); err != nil || v != "test-key" {
		t.Fatalf("secret key: %q %v", v, err)
	}
}

func TestScalewayOverrides(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_SCALEWAY_SECRET_KEY":      "test-key",
		"YAGURA_SCALEWAY_ORGANIZATION_ID": " 0000000A-0000-0000-0000-00000000000b ",
		"YAGURA_SCALEWAY_INTERVAL":        "3h",
	}))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Scaleway
	if s.OrganizationID != "0000000A-0000-0000-0000-00000000000b" || s.Interval != 3*time.Hour {
		t.Fatalf("unexpected config: %+v", s)
	}
}

func TestScalewaySecretKeyFile(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_SCALEWAY_SECRET_KEY_FILE": writeFile(t, "file-key\r\n"),
		"YAGURA_SCALEWAY_ORGANIZATION_ID": testOrganizationID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Scaleway == nil {
		t.Fatal("Scaleway disabled with a secret key file set")
	}
	if v, err := c.Scaleway.SecretKey.Read(); err != nil || v != "file-key" {
		t.Fatalf("secret key: %q %v", v, err)
	}
}

func TestScalewayValidation(t *testing.T) {
	bad := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"key and file", map[string]string{
			"YAGURA_SCALEWAY_SECRET_KEY":      "s3cretpw",
			"YAGURA_SCALEWAY_SECRET_KEY_FILE": "/f",
			"YAGURA_SCALEWAY_ORGANIZATION_ID": testOrganizationID,
		}, "YAGURA_SCALEWAY_SECRET_KEY_FILE"},
		{"interval too short", map[string]string{
			"YAGURA_SCALEWAY_SECRET_KEY":      "s3cretpw",
			"YAGURA_SCALEWAY_ORGANIZATION_ID": testOrganizationID,
			"YAGURA_SCALEWAY_INTERVAL":        "10s",
		}, "YAGURA_SCALEWAY_INTERVAL"},
		{"interval unparseable", map[string]string{
			"YAGURA_SCALEWAY_SECRET_KEY":      "s3cretpw",
			"YAGURA_SCALEWAY_ORGANIZATION_ID": testOrganizationID,
			"YAGURA_SCALEWAY_INTERVAL":        "soon",
		}, "YAGURA_SCALEWAY_INTERVAL"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(mapLookup(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error naming %s, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "s3cretpw") {
				t.Fatalf("error leaks the key: %v", err)
			}
		})
	}
}

func TestScalewayOrganizationIDMustBeUUID(t *testing.T) {
	for _, id := range []string{
		"s3cretpw",
		"00000000-0000-0000-0000-00000000000",
		"00000000-0000-0000-0000-000000000001&page=s3cretpw",
		"00000000-0000-0000-0000-000000000001 s3cretpw",
		"0000000g-0000-0000-0000-000000000001",
		"000000000000000000000000000000000001",
	} {
		t.Run(id, func(t *testing.T) {
			_, err := Load(mapLookup(map[string]string{
				"YAGURA_SCALEWAY_SECRET_KEY":      "test-key",
				"YAGURA_SCALEWAY_ORGANIZATION_ID": id,
			}))
			if err == nil || !strings.Contains(err.Error(), "YAGURA_SCALEWAY_ORGANIZATION_ID") {
				t.Fatalf("want error naming YAGURA_SCALEWAY_ORGANIZATION_ID, got %v", err)
			}
			if strings.Contains(err.Error(), "s3cretpw") || strings.Contains(err.Error(), id) {
				t.Fatalf("error echoes the value: %v", err)
			}
		})
	}
}
