package clientserver

import (
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	clientassets "github.com/wseternal/ssetunnel/frontend/client"
)

// serveIndex serves the SPA document, read once at startup. The strict
// contract headers come from the securityHeaders wrapper; the committed
// index.html has no inline script, so script-src 'self' is safe.
func (s *Server) serveIndex(w http.ResponseWriter, _ *http.Request) {
	if s.indexHTML == nil {
		http.Error(w, "client bundle missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.indexHTML)
}

// serveAsset serves one content-hashed Vite asset from the embedded FS with
// immutable caching and an ETag for header-only 304 revalidation. Mirrors
// the console's dedicated asset handler (internal/consoleserver): embed.FS
// reports a zero ModTime, so without an explicit ETag ServeFileFS cannot
// emit 304 at all.
func (s *Server) serveAsset(w http.ResponseWriter, req *http.Request) {
	// ClientWebRootFs is fs.Sub(...), whose Open rejects any path that fails
	// fs.ValidPath — which forbids a leading slash, so trim before use.
	name := strings.TrimPrefix(path.Clean(req.URL.Path), "/")
	// Keep the resolved name inside assets/: path.Clean runs before the
	// ValidPath gate, so /assets/../index.html would otherwise name the FS
	// root. mux's 301 cleanup and ServeFileFS's dot-dot check also stop it;
	// the containment is stated explicitly here.
	if !strings.HasPrefix(name, "assets/") || !fs.ValidPath(name) {
		http.NotFound(w, req)
		return
	}
	info, err := fs.Stat(clientassets.ClientWebRootFs, name)
	if err != nil {
		// Quote the error: it embeds the request path verbatim, and an
		// unescaped newline would forge extra log records.
		log.Printf("ui: stat asset %q failed: %q", name, err.Error())
		http.NotFound(w, req)
		return
	}
	if info.IsDir() {
		http.NotFound(w, req)
		return
	}
	ct := mime.TypeByExtension(path.Ext(name))
	hashed := ct != ""
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Vary", "Accept-Encoding")
	h.Set("Content-Type", ct)
	if hashed {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// A guessed type must not be pinned for a year: nosniff makes the
		// browser refuse it, and immutable would cache the broken answer.
		h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	}
	// The filename is a content hash, so it doubles as a validator.
	h.Set("ETag", strconv.Quote(path.Base(name)))
	http.ServeFileFS(w, req, clientassets.ClientWebRootFs, name)
}
