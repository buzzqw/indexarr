package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipPool reuses writers across requests. The pages are rendered on every
// request, so allocating a compressor per response would waste the gain.
var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

// withCompression gzips the responses that can benefit from it. The decision
// waits for the handler to set Content-Type, so JSON, HTML, CSS, JS, XML and
// SVG are compressed while images, backups and downloads pass through.
func withCompression(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Range requests must keep their exact byte offsets, and HEAD has no
		// body to compress.
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" ||
			!acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressResponseWriter{ResponseWriter: w}
		defer cw.close()
		next.ServeHTTP(cw, r)
	})
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}

type compressResponseWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	decided  bool
	compress bool
}

func (w *compressResponseWriter) WriteHeader(status int) {
	if w.decided {
		return
	}
	w.decided = true
	if status != http.StatusNoContent && status != http.StatusNotModified &&
		w.Header().Get("Content-Encoding") == "" &&
		compressible(w.Header().Get("Content-Type")) {
		w.compress = true
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Del("Content-Length")
		w.gz = gzipPool.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *compressResponseWriter) Write(p []byte) (int, error) {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.compress {
		return w.gz.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// Flush keeps long downloads and any streaming handler working through the
// wrapper.
func (w *compressResponseWriter) Flush() {
	if w.compress && w.gz != nil {
		_ = w.gz.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *compressResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *compressResponseWriter) close() {
	if w.gz == nil {
		return
	}
	_ = w.gz.Close()
	w.gz.Reset(io.Discard)
	gzipPool.Put(w.gz)
	w.gz = nil
}

func compressible(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if index := strings.IndexByte(ct, ';'); index >= 0 {
		ct = strings.TrimSpace(ct[:index])
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/javascript", "application/xml",
		"application/manifest+json", "application/rss+xml", "application/atom+xml",
		"image/svg+xml":
		return true
	default:
		return false
	}
}
