package web

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
)

func (s *Server) maintenanceBackupNow(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		redirect(w, r, "/maintenance", "", err.Error())
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Minute)
	defer cancel()
	info, err := backup.Create(ctx, s.store, s.store.Path())
	if err != nil {
		redirect(w, r, "/maintenance", "", err.Error())
		return
	}
	if removed, err := backup.Prune(s.store.Path(), settings.BackupKeep); err != nil {
		s.log.Warn("backup rotation failed", "component", "indexarr", "subsystem", "backup", "error", err)
	} else if removed > 0 {
		s.log.Info("old backups removed", "count", removed)
	}
	s.log.Info("manual backup created", "file", info.Name, "bytes", info.Size)
	redirect(w, r, "/maintenance", "Backup created: "+info.Name, "")
}

func (s *Server) maintenanceBackupDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, err := backup.Resolve(s.store.Path(), name)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "backup not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, filepath.Clean(path))
}

func (s *Server) maintenanceBackupDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := backup.Delete(s.store.Path(), name); err != nil {
		redirect(w, r, "/maintenance", "", err.Error())
		return
	}
	redirect(w, r, "/maintenance", "Backup deleted: "+name, "")
}

func (s *Server) maintenanceRestore(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		redirect(w, r, "/maintenance", "", "Invalid upload: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("backup")
	if err != nil {
		redirect(w, r, "/maintenance", "", "Choose a .db backup file")
		return
	}
	defer file.Close()
	if header.Size > 512<<20 {
		redirect(w, r, "/maintenance", "", "The backup is larger than 512 MiB")
		return
	}
	dir := backup.Dir(s.store.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		redirect(w, r, "/maintenance", "", err.Error())
		return
	}
	temp, err := os.CreateTemp(dir, "upload-*.db")
	if err != nil {
		redirect(w, r, "/maintenance", "", err.Error())
		return
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	written, err := io.Copy(temp, io.LimitReader(file, 512<<20+1))
	closeErr := temp.Close()
	if err != nil || closeErr != nil {
		redirect(w, r, "/maintenance", "", "Cannot save the upload")
		return
	}
	if written > 512<<20 {
		redirect(w, r, "/maintenance", "", "The backup is larger than 512 MiB")
		return
	}
	if err := backup.Validate(tempName); err != nil {
		redirect(w, r, "/maintenance", "", "Invalid backup: "+err.Error())
		return
	}
	if err := backup.Stage(s.store.Path(), tempName); err != nil {
		redirect(w, r, "/maintenance", "", err.Error())
		return
	}
	s.log.Info("restore staged; restarting to apply it", "file", header.Filename)
	message := "Backup validato e pronto: riavvia il servizio per applicarlo."
	if s.restart != nil {
		message = "Backup validato: il servizio si riavvierà per applicarlo."
		go func() {
			time.Sleep(time.Second)
			s.restart()
		}()
	}
	redirect(w, r, "/maintenance", message, "")
}

// contextWithTimeout keeps the cancellation of the request but detaches the
// deadline, for maintenance work that must survive a client disconnect.
func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
}
