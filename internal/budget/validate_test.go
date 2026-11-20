package budget

import (
	"strings"
	"testing"
)

func TestParseAmount(t *testing.T) {
	ok := map[string]float64{
		"300":      300,
		"300.5":    300.5,
		"300,5":    300.5,
		"300,50":   300.5,
		" 250 ":    250,
		"0.01":     0.01,
		"100000":   100000,
		"99999.99": 99999.99,
		"007":      7,
	}
	for in, want := range ok {
		got, err := ParseAmount(in)
		if err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	bad := []string{
		"", " ", "0", "0.00", "-5", "+5", "100000.01", "1000000", "1e3", "0x10",
		"300.123", "300,", ".5", "3 000", "1_000", "NaN", "Inf", "300.5.1", "300,5,1", "abc",
	}
	for _, in := range bad {
		if got, err := ParseAmount(in); err == nil {
			t.Errorf("ParseAmount(%q) = %v, want an error", in, got)
		}
	}
}

func TestValidProject(t *testing.T) {
	for _, s := range []string{"acme", "big-suite", "a", "0", "x-", strings.Repeat("a", 63)} {
		if !ValidProject(s) {
			t.Errorf("ValidProject(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "-a", "Acme", "big suite", "a_b", "café", strings.Repeat("a", 64), "a\n"} {
		if ValidProject(s) {
			t.Errorf("ValidProject(%q) = true, want false", s)
		}
	}
}

func TestNormalizeProjectID(t *testing.T) {
	got, ok := NormalizeProjectID(" 0A1B2C3D-0000-4000-8000-00000000000F ")
	if !ok || got != "0a1b2c3d-0000-4000-8000-00000000000f" {
		t.Errorf("NormalizeProjectID = %q, %v", got, ok)
	}
	for _, s := range []string{"", "acme", "0a1b2c3d-0000-4000-8000-00000000000", "0a1b2c3d00004000800000000000000f"} {
		if _, ok := NormalizeProjectID(s); ok {
			t.Errorf("NormalizeProjectID(%q) accepted", s)
		}
	}
}

func TestAutoProject(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	cases := map[string]string{
		// Name shapes the rule must handle: one word, dashes, spaces, case.
		"Acme":             "acme",
		"acme-backups":     "acme",
		"acme-api-staging": "acme",
		"nova-staging":     "nova",
		"Big Suite":        "big-suite",
		"Orbit":            "orbit",
		"default":          "default",
		// Edge cases: empty, trimmed, and the project_id fallback.
		"":                      id,
		"   ":                   id,
		" Acme ":                "acme",
		"-leading":              id,
		" -leading":             id,
		"Acme - prod":           "acme",
		"Acme -prod":            "acme",
		"Big Suite - staging":   "big-suite",
		"Big  Suite-x":          "big--suite",
		"Café":                  id,
		"acme_prod":             id,
		"Tab\tName":             id,
		strings.Repeat("a", 64): id,
		// A deleted project is billed with its ID as name: the ID is kept
		// whole, never cut at its first "-".
		id:                  id,
		" " + id + " ":      id,
		strings.ToUpper(id): id,
	}
	for name, want := range cases {
		if got := AutoProject(name, id); got != want {
			t.Errorf("AutoProject(%q) = %q, want %q", name, got, want)
		}
	}
}
