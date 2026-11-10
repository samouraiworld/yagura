package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const redacted = "[REDACTED]"

// Secret is a credential given inline (X) or through a file (X_FILE).
// File-backed secrets are re-read on every Read call so that a rotated
// mounted secret is picked up without restarting.
type Secret struct {
	name   string // variable name the user actually set
	inline string
	file   string
}

// IsSet reports whether the secret was configured, inline or by file.
func (s Secret) IsSet() bool {
	return s.inline != "" || s.file != ""
}

// Read returns the secret value with surrounding spaces, CR and LF trimmed.
// Errors name the variable and never include the value or file content.
func (s Secret) Read() (string, error) {
	raw := s.inline
	if s.file != "" {
		b, err := os.ReadFile(s.file)
		if err != nil {
			return "", fmt.Errorf("%s: cannot read secret file: %w", s.name, err)
		}
		raw = string(b)
	}
	v := strings.Trim(raw, " \t\r\n")
	if v == "" {
		return "", fmt.Errorf("%s: secret is empty", s.name)
	}
	return v, nil
}

// String redacts the value so a Secret never leaks through fmt.
func (s Secret) String() string { return redacted }

// LogValue redacts the value so a Secret never leaks through slog.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// GoString redacts the value for the %#v verb.
func (s Secret) GoString() string { return redacted }
