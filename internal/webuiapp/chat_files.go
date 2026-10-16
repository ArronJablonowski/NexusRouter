package webuiapp

import (
	"context"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/webui"
)

func (h *Handler) serveChatFiles(w http.ResponseWriter, r *http.Request) bool {
	prefix := h.basePath + "/api/v1/chats/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if len(parts) < 2 || parts[1] != "files" {
		return false
	}
	if !h.authenticated(r) {
		h.writeError(w, r, 401, "session_required")
		return true
	}
	if !strictBrowserGET(r) || r.URL.RawQuery != "" || !webui.ValidID(parts[0]) {
		h.writeError(w, r, 400, "invalid_request")
		return true
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.writeError(w, r, 503, "browser_capacity")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	// Generated documents never execute as application-origin HTML or script.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if len(parts) == 2 {
		if h.reads.ChatFiles == nil {
			h.writeError(w, r, 503, "files_unavailable")
			return true
		}
		page, e := safeChatFiles(ctx, h.reads.ChatFiles, parts[0])
		if e != nil || page.Validate() != nil || page.ChatID != parts[0] {
			h.writeError(w, r, 503, "files_unavailable")
			return true
		}
		h.writeJSON(w, 200, page)
		return true
	}
	if len(parts) != 4 || !webui.ValidID(parts[2]) || (parts[3] != "download" && parts[3] != "preview") || h.reads.ChatFile == nil {
		h.writeError(w, r, 404, "file_unavailable")
		return true
	}
	f, b, e := safeChatFile(ctx, h.reads.ChatFile, parts[0], parts[2])
	if e != nil || f.Validate() != nil || f.ID != parts[2] || len(b) != f.Size {
		h.writeError(w, r, 404, "file_unavailable")
		return true
	}
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	disposition := "attachment"
	kind := "application/octet-stream"
	if parts[3] == "preview" {
		kind = http.DetectContentType(b)
		if kind != f.MediaType || (kind != "image/png" && kind != "image/jpeg" && kind != "image/webp") {
			h.writeError(w, r, 415, "preview_unavailable")
			return true
		}
		disposition = "inline"
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
	return true
}
func safeChatFiles(ctx context.Context, read func(context.Context, string) (webui.ChatFilePage, error), chat string) (p webui.ChatFilePage, e error) {
	defer func() {
		if recover() != nil {
			e = webui.ErrContract
		}
	}()
	return read(ctx, chat)
}
func safeChatFile(ctx context.Context, read func(context.Context, string, string) (webui.ChatFile, []byte, error), chat, id string) (f webui.ChatFile, b []byte, e error) {
	defer func() {
		if recover() != nil {
			e = webui.ErrContract
		}
	}()
	return read(ctx, chat, id)
}
