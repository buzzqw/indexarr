// Package backup creates, lists and restores the SQLite database. A restore is
// staged as <db>.restore and applied by the next process start, so the live
// database is never overwritten while the service is using it.
package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/buzzqw/indexarr/internal/store"
)

const (
	automaticPrefix  = "indexarr-"
	preRestorePrefix = "pre-restore-"
	zipSuffix        = ".zip"
	dbSuffix         = ".db"
	// maxExtractBytes bounds a backup archive so a malicious upload cannot
	// expand into the whole disk.
	maxExtractBytes = 2 << 30
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
		if entry.IsDir() || !isBackupName(entry.Name()) {
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

func isBackupName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, zipSuffix) || strings.HasSuffix(lower, dbSuffix)
}

// Create writes a consistent automatic backup as a zip archive and returns its
// info. The archive holds one .db file: deflate shrinks the SQLite pages a lot
// and the operator can still restore the plain file after unpacking it.
func Create(ctx context.Context, st *store.Store, dbPath string) (Info, error) {
	dir := Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Info{}, err
	}
	base := automaticPrefix + time.Now().Format("20060102-150405")
	target := filepath.Join(dir, base+zipSuffix)
	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
			break
		}
		target = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, suffix, zipSuffix))
	}
	if err := ZipDatabase(ctx, st, target); err != nil {
		return Info{}, err
	}
	// VACUUM INTO materialises a full copy of the database in the process and
	// leaves the pages behind in the Go heap until the next scavenge. A 10 MiB
	// database costs about 9 MiB of RSS that way, and the process keeps it for
	// many minutes. Returning it to the OS right after the copy keeps the
	// backup feature without the permanent footprint. The backup file is
	// already complete at this point, so this only releases memory.
	debug.FreeOSMemory()
	info, err := os.Stat(target)
	if err != nil {
		return Info{}, err
	}
	return Info{Name: filepath.Base(target), Size: info.Size(), ModTime: info.ModTime(), Automatic: true}, nil
}

// ZipDatabase writes a consistent copy of the database into target as a zip
// archive. The temporary .db file lives next to the target and is removed even
// when compression fails.
func ZipDatabase(ctx context.Context, st *store.Store, target string) error {
	dir := filepath.Dir(target)
	temp, err := os.CreateTemp(dir, "indexarr-backup-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	_ = os.Remove(tempName)
	defer os.Remove(tempName)
	if err := st.BackupTo(ctx, tempName); err != nil {
		return err
	}
	part := target + ".part"
	if err := writeZip(tempName, part); err != nil {
		_ = os.Remove(part)
		return err
	}
	if err := os.Rename(part, target); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}

// CompressExisting converts the old plain .db backups into the zip format.
// It is safe to run at every start: once the directory holds only .zip files
// it does nothing. A file is deleted only after its zip archive validates.
func CompressExisting(dbPath string) (int, error) {
	dir := Dir(dbPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	compressed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), dbSuffix) {
			continue
		}
		source := filepath.Join(dir, entry.Name())
		target := strings.TrimSuffix(source, filepath.Ext(source)) + zipSuffix
		if _, err := os.Stat(target); err == nil {
			if Validate(target) == nil {
				if os.Remove(source) == nil {
					compressed++
				}
			}
			continue
		}
		if err := writeZip(source, target); err != nil {
			continue
		}
		if err := Validate(target); err != nil {
			_ = os.Remove(target)
			continue
		}
		if err := os.Remove(source); err != nil {
			_ = os.Remove(target)
			continue
		}
		compressed++
	}
	return compressed, nil
}

// Export writes a zip archive to dir and returns its path. The caller serves
// the file and removes it: an on-demand download must not land in the rotation.
func Export(ctx context.Context, st *store.Store, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(dir, "indexarr-export-*.tmp")
	if err != nil {
		return "", err
	}
	target := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	_ = os.Remove(target)
	if err := ZipDatabase(ctx, st, target); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return target, nil
}

func writeZip(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	header := &zip.FileHeader{Name: filepath.Base(source), Method: zip.Deflate}
	header.SetModTime(info.ModTime())
	entry, err := zw.CreateHeader(header)
	if err == nil {
		_, err = io.Copy(entry, in)
	}
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(target)
	}
	return err
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
	if name == "" || filepath.Base(name) != name || !isBackupName(name) {
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
// indexarr backup must contain. A zip archive is unpacked first, so both the
// old .db backups and the new .zip backups remain valid.
func Validate(path string) error {
	dbPath, cleanup, err := extractDatabase(path)
	if err != nil {
		return err
	}
	defer cleanup()
	db, err := sql.Open("sqlite", sqliteFileDSN(dbPath))
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
// running process keeps using the old file; the next start applies it. A zip
// upload is unpacked first.
func Stage(dbPath, source string) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	dbPathSource, cleanup, err := extractDatabase(source)
	if err != nil {
		return err
	}
	defer cleanup()
	return copyFile(dbPathSource, dbPath+".restore")
}

// extractDatabase returns a readable SQLite file for path. When path is a zip
// archive it writes the first .db entry to a temporary file next to the source
// and returns the cleanup that removes it. A plain database is returned as is.
func extractDatabase(path string) (string, func(), error) {
	file, err := os.Open(path)
	if err != nil {
		return "", func() {}, err
	}
	var magic [4]byte
	_, readErr := io.ReadFull(file, magic[:])
	_ = file.Close()
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", func() {}, readErr
	}
	if string(magic[:]) != "PK\x03\x04" {
		return path, func() {}, nil
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return "", func() {}, fmt.Errorf("open backup archive: %w", err)
	}
	defer archive.Close()
	var entry *zip.File
	for _, candidate := range archive.File {
		if candidate.FileInfo().IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(candidate.Name), dbSuffix) {
			entry = candidate
			break
		}
		if entry == nil {
			entry = candidate
		}
	}
	if entry == nil {
		return "", func() {}, fmt.Errorf("backup archive holds no database")
	}
	if entry.UncompressedSize64 > maxExtractBytes {
		return "", func() {}, fmt.Errorf("backup database is larger than %d MiB", maxExtractBytes>>20)
	}
	source, err := entry.Open()
	if err != nil {
		return "", func() {}, err
	}
	defer source.Close()
	temp, err := os.CreateTemp(filepath.Dir(path), "extract-*.tmp")
	if err != nil {
		return "", func() {}, err
	}
	tempName := temp.Name()
	cleanup := func() { _ = os.Remove(tempName) }
	written, err := io.Copy(temp, io.LimitReader(source, maxExtractBytes+1))
	closeErr := temp.Close()
	if err != nil || closeErr != nil {
		cleanup()
		if err != nil {
			return "", func() {}, err
		}
		return "", func() {}, closeErr
	}
	if written > maxExtractBytes {
		cleanup()
		return "", func() {}, fmt.Errorf("backup database is larger than %d MiB", maxExtractBytes>>20)
	}
	return tempName, cleanup, nil
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
		target := filepath.Join(dir, preRestorePrefix+time.Now().Format("20060102-150405")+zipSuffix)
		if err := writeZip(dbPath, target); err != nil {
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
