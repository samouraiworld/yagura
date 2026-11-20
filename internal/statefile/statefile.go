// Package statefile reads and writes yagura's state files: each store is one
// regular file in the state directory, replaced atomically on every save.
package statefile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// ProbePattern names the temporary file ProbeWritable creates.
const ProbePattern = ".probe-*"

// ReadRegular reads path, which must be a regular file of at most maxBytes.
// A symlink, FIFO or device is refused: a FIFO would block startup and a
// symlink would read a file outside the state directory. A missing file
// returns an error that wraps fs.ErrNotExist.
func ReadRegular(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path) //nolint:gosec // G304: the path is the operator's state directory
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// The file opened must be the regular file checked above, not one put
	// in its place in between.
	if opened, err := f.Stat(); err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%s changed while it was opened", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s is larger than %d KiB", path, maxBytes>>10)
	}
	return data, nil
}

// WriteAtomic replaces path with data: a temporary file named by pattern in
// the same directory is written with mode 0600, synced and renamed over path,
// then the directory is synced so that the rename survives a crash. On error
// path keeps its previous content and the temporary file is removed.
func WriteAtomic(path, pattern string, data []byte, logger *slog.Logger) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	// The data is saved; a failed directory sync only weakens its durability
	// after a crash, so it is logged, not returned.
	d, err := os.Open(dir) //nolint:gosec // G304: the operator's state directory
	if err == nil {
		err = d.Sync()
		_ = d.Close()
	}
	if err != nil {
		logger.Warn("cannot sync the state directory after a save", "path", path, "error", err)
	}
	return nil
}

// RemoveLeftovers removes the files of dir that match one of patterns, such
// as temporary files a crash during a save left behind. It is best effort.
func RemoveLeftovers(dir string, patterns []string, logger *slog.Logger) {
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, m := range matches {
			if err := os.Remove(m); err != nil && !errors.Is(err, fs.ErrNotExist) {
				logger.Warn("cannot remove a leftover temporary file", "path", m, "error", err)
			}
		}
	}
}

// ProbeWritable creates and removes a temporary file in dir.
func ProbeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ProbePattern)
	if err != nil {
		return fmt.Errorf("state directory is not writable: %w", err)
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
