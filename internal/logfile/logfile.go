// Package logfile writes the log to a file that rotates by size: when the
// current file would grow past the limit it becomes <name>.1, the older ones
// shift by one and the oldest beyond the kept count is removed. indexarr runs
// for weeks without a restart, so the log on disk must stay bounded on its
// own, without logrotate or journald limits.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Rotator is an io.Writer safe for concurrent use. One Write is never split
// across two files, so a log record always stays whole.
type Rotator struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int // files in total, the current one included
	file     *os.File
	size     int64
}

// Open opens (or creates) path for appending. maxBytes is the size of one
// file and keep the number of files kept, the current one included; keep is
// at least 1.
func Open(path string, maxBytes int64, keep int) (*Rotator, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("log file: size limit must be positive")
	}
	if keep < 1 {
		keep = 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("log file: %w", err)
	}
	r := &Rotator{path: path, maxBytes: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

// Path is the current log file.
func (r *Rotator) Path() string { return r.path }

func (r *Rotator) open() error {
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("log file: %w", err)
	}
	r.file, r.size = file, info.Size()
	return nil
}

// Write appends p, rotating first when p would take the file past the limit.
// A record larger than the limit still goes whole into a fresh file.
func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate shifts <name>.i to <name>.i+1, drops the file beyond the kept count
// and starts a new current file.
func (r *Rotator) rotate() error {
	if err := r.file.Close(); err != nil {
		return fmt.Errorf("log file: %w", err)
	}
	r.file = nil
	if r.keep == 1 {
		if err := os.Remove(r.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("log file: %w", err)
		}
		return r.open()
	}
	_ = os.Remove(r.backup(r.keep - 1))
	for i := r.keep - 2; i >= 1; i-- {
		if err := os.Rename(r.backup(i), r.backup(i+1)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("log file: %w", err)
		}
	}
	if err := os.Rename(r.path, r.backup(1)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("log file: %w", err)
	}
	return r.open()
}

func (r *Rotator) backup(i int) string { return r.path + "." + strconv.Itoa(i) }

// Close closes the current file. Later writes fail with os.ErrClosed.
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
