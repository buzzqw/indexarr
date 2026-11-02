package adminapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// downloadConfirmedEvent is the event name of the confirmation contract. The
// client sends it after the payload reaches 100%, so indexarr can tell a grab
// that was merely handed over from a file that really arrived.
const downloadConfirmedEvent = "download.completed"

// maxConfirmationBytes bounds the confirmation body: the contract carries one
// download, not a batch.
const maxConfirmationBytes = 64 << 10

type downloadConfirmationRequest struct {
	Event       string `json:"event"`
	Protocol    string `json:"protocol"`
	Hash        string `json:"hash"`
	Title       string `json:"title"`
	Client      string `json:"client"`
	Size        int64  `json:"size"`
	CompletedAt string `json:"completed_at"`
}

type downloadConfirmationResponse struct {
	Event   string `json:"event"`
	Matched bool   `json:"matched"`
	GrabID  int64  `json:"grab_id,omitempty"`
}

// handleDownloadConfirm records the completion a download client reports.
// Authentication is the same API key as the rest of the admin API: the key
// that already lets the client search can also say which download finished.
func (s *Server) handleDownloadConfirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxConfirmationBytes)
	var in downloadConfirmationRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if event := strings.TrimSpace(in.Event); event != "" && event != downloadConfirmedEvent {
		writeError(w, http.StatusBadRequest, "unknown event")
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(in.Protocol))
	switch protocol {
	case "torrent":
		if !isHexHash(in.Hash, 40) {
			writeError(w, http.StatusBadRequest, "torrent hash must be 40 hexadecimal characters")
			return
		}
	case "ed2k":
		if !isHexHash(in.Hash, 32) {
			writeError(w, http.StatusBadRequest, "eD2k hash must be 32 hexadecimal characters")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "protocol must be torrent or ed2k")
		return
	}
	completed := time.Time{}
	if raw := strings.TrimSpace(in.CompletedAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "completed_at must be RFC 3339")
			return
		}
		completed = parsed
	}
	size := in.Size
	if size < 0 {
		size = 0
	}

	grabID, err := s.store.ConfirmDownload(r.Context(), store.DownloadConfirmation{
		Protocol:    protocol,
		Hash:        strings.TrimSpace(in.Hash),
		Title:       strings.TrimSpace(in.Title),
		Client:      strings.TrimSpace(in.Client),
		Size:        size,
		CompletedAt: completed,
	})
	if err != nil {
		s.logger.Error("adminapi: confirm download", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if grabID > 0 {
		s.logger.Debug("adminapi: download confirmed",
			"grab_id", grabID, "client", strings.TrimSpace(in.Client), "protocol", protocol)
	} else {
		s.logger.Debug("adminapi: download confirmation did not match a grab",
			"client", strings.TrimSpace(in.Client), "protocol", protocol)
	}
	writeJSON(w, http.StatusOK, downloadConfirmationResponse{
		Event:   downloadConfirmedEvent,
		Matched: grabID > 0,
		GrabID:  grabID,
	})
}

// isHexHash reports whether value is exactly size hexadecimal characters.
func isHexHash(value string, size int) bool {
	value = strings.TrimSpace(value)
	if len(value) != size {
		return false
	}
	for _, ch := range value {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		case ch >= 'A' && ch <= 'F':
		default:
			return false
		}
	}
	return true
}
