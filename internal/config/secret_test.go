package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSecretInlineAndFile(t *testing.T) {
	s, err := secret(mapLookup(map[string]string{"X": "inline"}), "X")
	if err != nil || !s.IsSet() {
		t.Fatal(err)
	}
	if v, err := s.Read(); err != nil || v != "inline" {
		t.Fatalf("inline: %q %v", v, err)
	}
	s, err = secret(mapLookup(map[string]string{"X_FILE": writeFile(t, "fromfile")}), "X")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.Read(); err != nil || v != "fromfile" {
		t.Fatalf("file: %q %v", v, err)
	}
	_, err = secret(mapLookup(map[string]string{"X": "a", "X_FILE": "/f"}), "X")
	if err == nil || !strings.Contains(err.Error(), "X") || !strings.Contains(err.Error(), "X_FILE") {
		t.Fatalf("both set: %v", err)
	}
	if s, _ := secret(mapLookup(nil), "X"); s.IsSet() {
		t.Fatal("unset secret reports IsSet")
	}
}

func TestSecretFileTrimmedAndEmpty(t *testing.T) {
	s, _ := secret(mapLookup(map[string]string{"X_FILE": writeFile(t, "tok\r\n")}), "X")
	if v, err := s.Read(); err != nil || v != "tok" {
		t.Fatalf("trim: %q %v", v, err)
	}
	s, _ = secret(mapLookup(map[string]string{"X_FILE": writeFile(t, "  \n")}), "X")
	if _, err := s.Read(); err == nil || !strings.Contains(err.Error(), "X_FILE") {
		t.Fatalf("empty: %v", err)
	}
	s, _ = secret(mapLookup(map[string]string{"X_FILE": filepath.Join(t.TempDir(), "missing")}), "X")
	if _, err := s.Read(); err == nil || !strings.Contains(err.Error(), "X_FILE") {
		t.Fatalf("missing: %v", err)
	}
	s, _ = secret(mapLookup(map[string]string{"X": " \r\n"}), "X")
	if _, err := s.Read(); err == nil || !strings.Contains(err.Error(), "X") {
		t.Fatalf("empty inline: %v", err)
	}
}

func TestSecretReReadsFile(t *testing.T) {
	p := writeFile(t, "a")
	s, _ := secret(mapLookup(map[string]string{"X_FILE": p}), "X")
	if v, _ := s.Read(); v != "a" {
		t.Fatalf("got %q", v)
	}
	if err := os.WriteFile(p, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Read(); v != "b" {
		t.Fatalf("got %q", v)
	}
}

func TestSecretNeverPrinted(t *testing.T) {
	const value = "super-secret-value"
	s, _ := secret(mapLookup(map[string]string{"X": value}), "X")
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v", s, s, struct{ S Secret }{s})} {
		if strings.Contains(out, value) {
			t.Fatalf("fmt leaked secret: %q", out)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("msg", "token", s)
	if strings.Contains(buf.String(), value) || !strings.Contains(buf.String(), redacted) {
		t.Fatalf("slog output: %q", buf.String())
	}
}

// holder keeps a Secret in an unexported field: fmt cannot call methods on
// it and falls back to printing its fields.
type holder struct {
	name  string
	token Secret
}

func TestSecretUnprintableInAnyForm(t *testing.T) {
	const value = "super-secret-value"
	s, _ := secret(mapLookup(map[string]string{"X": value}), "X")
	h := holder{name: "demo", token: s}
	var outs []string
	for _, verb := range []string{"%d", "%s", "%q", "%x", "%X", "%v", "%+v", "%#v"} {
		outs = append(outs, fmt.Sprintf(verb, s), fmt.Sprintf(verb, h), fmt.Sprintf(verb, &h))
	}
	var text, js bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("msg", "holder", h)
	slog.New(slog.NewJSONHandler(&js, nil)).Info("msg", "holder", h)
	outs = append(outs, text.String(), js.String())
	hexValue := fmt.Sprintf("%x", value)
	for _, out := range outs {
		if strings.Contains(out, value) || strings.Contains(strings.ToLower(out), hexValue) {
			t.Errorf("secret leaked: %q", out)
		}
	}
}

func TestSecretFileSizeCapped(t *testing.T) {
	s, _ := secret(mapLookup(map[string]string{"X_FILE": writeFile(t, strings.Repeat("a", 64<<10))}), "X")
	if v, err := s.Read(); err != nil || len(v) != 64<<10 {
		t.Fatalf("64 KiB file: len %d, err %v", len(v), err)
	}
	s, _ = secret(mapLookup(map[string]string{"X_FILE": writeFile(t, strings.Repeat("a", 64<<10+1))}), "X")
	_, err := s.Read()
	if err == nil || !strings.Contains(err.Error(), "X_FILE") {
		t.Fatalf("oversized file: err = %v, want an error naming X_FILE", err)
	}
	if strings.Contains(err.Error(), "aaaa") {
		t.Fatalf("error leaks file content: %v", err)
	}
}
