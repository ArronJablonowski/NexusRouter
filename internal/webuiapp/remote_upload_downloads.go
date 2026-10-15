package webuiapp

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/chatfiles"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (h *Handler) serveRemoteFiles(w http.ResponseWriter, r *http.Request) bool {
	prefix := h.basePath + "/api/v1/remote-files/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	if !h.authenticated(r) {
		h.writeError(w, r, 401, "session_required")
		return true
	}
	p := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if !strictBrowserGET(r) || r.URL.RawQuery != "" || (len(p) != 2 && len(p) != 4) || !remoteControlID(p[0], 1) || !remoteControlID(p[1], 16) {
		h.writeError(w, r, 400, "invalid_request")
		return true
	}
	d, ok := h.remoteDispatcher.(*RecordedRemoteDispatcher)
	if !ok || d.UploadDirectory == "" {
		h.writeError(w, r, 404, "file_unavailable")
		return true
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.writeError(w, r, 503, "browser_capacity")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	store, e := chatfiles.Open(ctx, d.UploadDirectory)
	if e != nil {
		h.writeError(w, r, 503, "files_unavailable")
		return true
	}
	defer store.Close()
	scope := remoteUploadScope(p[0], p[1])
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if len(p) == 2 {
		files, e := store.List(ctx, scope)
		if e != nil {
			h.writeError(w, r, 503, "files_unavailable")
			return true
		}
		h.writeJSON(w, 200, struct {
			Version   int              `json:"version"`
			Instance  string           `json:"instance"`
			RequestID string           `json:"request_id"`
			Files     []webui.ChatFile `json:"files"`
		}{1, p[0], p[1], files})
		return true
	}
	if !webui.ValidID(p[2]) || (p[3] != "download" && p[3] != "preview") {
		h.writeError(w, r, 404, "file_unavailable")
		return true
	}
	f, b, e := store.Get(ctx, scope, p[2])
	if e != nil {
		h.writeError(w, r, 404, "file_unavailable")
		return true
	}
	kind, disposition := "application/octet-stream", "attachment"
	if p[3] == "preview" {
		kind = http.DetectContentType(b)
		if kind != f.MediaType || (kind != "image/png" && kind != "image/jpeg") {
			h.writeError(w, r, 415, "preview_unavailable")
			return true
		}
		disposition = "inline"
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
	return true
}
