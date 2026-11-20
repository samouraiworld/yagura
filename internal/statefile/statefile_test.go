package statefile

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWriteAtomicAndReadRegular(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	logger := slog.New(slog.DiscardHandler)
	for _, content := range []string{"first\n", "second\n"} {
		if err := WriteAtomic(path, ".state-*.tmp", []byte(content), logger); err != nil {
			t.Fatal(err)
		}
		got, err := ReadRegular(path, 1<<10)
		if err != nil || string(got) != content {
			t.Fatalf("ReadRegular = %q, %v; want %q", got, err, content)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".state-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestWriteAtomicFailureKeepsOldFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	logger := slog.New(slog.DiscardHandler)
	if err := WriteAtomic(path, ".state-*.tmp", []byte("old"), logger); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: a read-only directory, on purpose
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // G302: restore the directory
	if err := WriteAtomic(path, ".state-*.tmp", []byte("new"), logger); err == nil {
		t.Fatal("WriteAtomic in a read-only directory: want an error")
	}
	if got, err := ReadRegular(path, 1<<10); err != nil || string(got) != "old" {
		t.Errorf("after a failed write: %q, %v; want the old content", got, err)
	}
	if err := ProbeWritable(dir); err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Errorf("ProbeWritable on a read-only directory = %v", err)
	}
}

func TestReadRegularErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadRegular(filepath.Join(dir, "missing"), 10); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v, want fs.ErrNotExist", err)
	}
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 11), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegular(big, 10); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("file over the cap: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(big, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegular(link, 100); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("symlink: %v", err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	if _, err := ReadRegular(fifo, 100); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("FIFO: %v", err)
	}
}

func TestRemoveLeftoversAndProbe(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "state.json")
	for _, name := range []string{".state-1.tmp", ".probe-2", "state.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	RemoveLeftovers(dir, []string{".state-*.tmp", ".probe-*"}, slog.New(slog.DiscardHandler))
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	hidden, _ := filepath.Glob(filepath.Join(dir, ".*"))
	if len(left) != 1 || left[0] != keep || len(hidden) != 0 {
		t.Errorf("after RemoveLeftovers: %v %v", left, hidden)
	}
	if err := ProbeWritable(dir); err != nil {
		t.Errorf("ProbeWritable: %v", err)
	}
	if hidden, _ := filepath.Glob(filepath.Join(dir, ".probe-*")); len(hidden) != 0 {
		t.Errorf("probe file left: %v", hidden)
	}
}
