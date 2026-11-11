package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
)

const (
	redacted = "[REDACTED]"
	// maxSecretFileBytes bounds what Read takes from an X_FILE file.
	maxSecretFileBytes = 64 << 10
)

// Secret is a credential given inline (X) or through a file (X_FILE).
// File-backed secrets are re-read on every Read call so that a rotated
// mounted secret is picked up without restarting.
//
// The data sits behind a pointer: when a Secret is an unexported field of a
// struct, fmt cannot call its methods and prints the fields instead, and a
// nested pointer prints as an address only. The inline value is a pointer
// too, because for a verb invalid on pointers (%s, %q) fmt reports the
// pointer by printing its target one level deep.
type Secret struct {
	d *secretData
}

type secretData struct {
	name   string // variable name the user actually set
	inline *string
	file   string
}

func newSecret(name, inline, file string) Secret {
	return Secret{d: &secretData{name: name, inline: &inline, file: file}}
}

// IsSet reports whether the secret was configured, inline or by file.
func (s Secret) IsSet() bool {
	return s.d != nil && ((s.d.inline != nil && *s.d.inline != "") || s.d.file != "")
}

// Read returns the secret value with surrounding whitespace (any Unicode
// space, including CR and LF) trimmed.
// Errors name the variable and never include the value or file content.
func (s Secret) Read() (string, error) {
	d := s.d
	if d == nil {
		d = &secretData{}
	}
	var raw string
	if d.inline != nil {
		raw = *d.inline
	}
	if d.file != "" {
		b, err := readSecretFile(d.file)
		if err != nil {
			// Drop the path: a token set as X_FILE by mistake would
			// otherwise reach the logs on every poll.
			var pe *fs.PathError
			if errors.As(err, &pe) {
				err = pe.Err
			}
			return "", fmt.Errorf("%s: cannot read secret file: %w", d.name, err)
		}
		if len(b) > maxSecretFileBytes {
			return "", fmt.Errorf("%s: secret file is larger than %d KiB", d.name, maxSecretFileBytes>>10)
		}
		raw = string(b)
	}
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", fmt.Errorf("%s: secret is empty", d.name)
	}
	return v, nil
}

// readSecretFile reads at most maxSecretFileBytes+1 bytes, so that the
// caller can tell an oversized file without loading all of it.
func readSecretFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is the operator's X_FILE setting
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxSecretFileBytes+1))
}

// Format redacts the value for every fmt verb and flag.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// String redacts the value so a Secret never leaks through fmt.
func (s Secret) String() string { return redacted }

// LogValue redacts the value so a Secret never leaks through slog.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// GoString redacts the value for the %#v verb.
func (s Secret) GoString() string { return redacted }
