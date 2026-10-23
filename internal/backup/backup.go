// Package backup creates, lists and restores the SQLite database. A restore is
// staged as <db>.restore and applied by the next process start, so the live
// database is never overwritten while the service is using it.
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/buzzqw/indexarr/internal/store"
)

const (
	automaticPrefix  = "indexarr-"
	preRestorePrefix = "pre-restore-"
)

// Info describes one file in the backup directory.
type Info struct {
	Name      string
	Size      int64
	ModTime   time.Time
	Automatic bool
}

// Dir returns the backup directory next to the database.
func Dir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "backups")
}

// List returns every .db file in the backup directory, newest first.
func List(dbPath string) ([]Info, error) {
	dir := Dir(dbPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{
			Name:      entry.Name(),
			Size:      info.Size(),
			ModTime:   info.ModTime(),
			Automatic: strings.HasPrefix(entry.Name(), automaticPrefix),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// LatestAutomatic returns the modification time of the newest automatic
// backup, or the zero time.
func LatestAutomatic(dbPath string) time.Time {
	entries, err := List(dbPath)
	if err != nil {
		return time.Time{}
	}
	for _, entry := range entries {
		if entry.Automatic {
			return entry.ModTime
		}
	}
	return time.Time{}
}

// Create writes a consistent automatic backup and returns its info.
func Create(ctx context.Context, st *store.Store, dbPath string) (Info, error) {
	dir := Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Info{}, err
	}
	base := automaticPrefix + time.Now().Format("20060102-150405")
	target := filepath.Join(dir, base+".db")
	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
			break
		}
		target = filepath.Join(dir, fmt.Sprintf("%s-%d.db", base, suffix))
	}
	if err := st.BackupTo(ctx, target); err != nil {
		return Info{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return Info{}, err
	}
	return Info{Name: filepath.Base(target), Size: info.Size(), ModTime: info.ModTime(), Automatic: true}, nil
}

// Prune removes the oldest automatic backups, keeping at most keep files.
// Pre-restore copies are never removed.
func Prune(dbPath string, keep int) (int, error) {
	if keep < 1 {
		keep = 1
	}
	entries, err := List(dbPath)
	if err != nil {
		return 0, err
	}
	var automatic []Info
	for _, entry := range entries {
		if entry.Automatic {
			automatic = append(automatic, entry)
		}
	}
	removed := 0
	for i := keep; i < len(automatic); i++ {
		if err := os.Remove(filepath.Join(Dir(dbPath), automatic[i].Name)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// Resolve validates a backup name from the web UI and returns its path.
func Resolve(dbPath, name string) (string, error) {
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".db") {
		return "", fmt.Errorf("invalid backup name")
	}
	path := filepath.Join(Dir(dbPath), name)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("invalid backup")
	}
	return path, nil
}

// Delete removes one backup file.
func Delete(dbPath, name string) error {
	path, err := Resolve(dbPath, name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Validate checks that path is a readable SQLite database with the tables a
// indexarr backup must contain.
func Validate(path string) error {
	db, err := sql.Open("sqlite", sqliteFileDSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		return fmt.Errorf("integrity check: %s", integrity)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('definitions','indexers','releases','settings')`).Scan(&tables); err != nil {
		return err
	}
	if tables != 4 {
		return fmt.Errorf("not an indexarr database (found %d required tables)", tables)
	}
	return nil
}

// Stage copies a validated upload next to the database as <db>.restore. The
// running process keeps using the old file; the next start applies it.
func Stage(dbPath, source string) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	return copyFile(source, dbPath+".restore")
}

// ApplyPending installs <db>.restore if it exists and is valid. It is called
// before the store is opened, so the current process has no handles on the
// database. A copy of the current file is kept in backups/.
func ApplyPending(dbPath string) (bool, error) {
	pending := dbPath + ".restore"
	if _, err := os.Stat(pending); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := Validate(pending); err != nil {
		return false, fmt.Errorf("restore backup is invalid: %w", err)
	}
	dir := Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	if _, err := os.Stat(dbPath); err == nil {
		target := filepath.Join(dir, preRestorePrefix+time.Now().Format("20060102-150405")+".db")
		if err := copyFile(dbPath, target); err != nil {
			return false, fmt.Errorf("save current database: %w", err)
		}
	}
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	if err := os.Rename(pending, dbPath); err != nil {
		// Cross-device fallback (the pending file is normally on the same
		// filesystem, so this is only for unusual layouts).
		if copyErr := copyFile(pending, dbPath); copyErr != nil {
			return false, fmt.Errorf("install restore: %w (copy: %v)", err, copyErr)
		}
		_ = os.Remove(pending)
	}
	return true, nil
}

func sqliteFileDSN(path string) string {
	replacer := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")
	return "file:" + replacer.Replace(path) + "?mode=ro"
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
