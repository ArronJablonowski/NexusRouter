package webuiapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/connectors/comfyui"
)

type ComfyService interface {
	Submit(context.Context, string, comfyui.Workflow) (comfyui.Job, error)
	Status(context.Context, string) (comfyui.Job, error)
	List() ([]comfyui.Job, error)
	CancelPending(context.Context, string) error
	Output(context.Context, string, string, int) ([]byte, error)
}

func (h *Handler) serveComfy(w http.ResponseWriter, r *http.Request) bool {
	prefix := h.basePath + "/api/v1/comfy"
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		return false
	}
	if !h.authenticated(r) {
		h.writeError(w, r, 401, "session_required")
		return true
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.writeError(w, r, 503, "browser_capacity")
		return true
	}
	if h.comfy == nil {
		h.writeError(w, r, 503, "comfy_unavailable")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	rel := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	if rel == "" && r.Method == "GET" {
		jobs, e := h.comfy.List()
		if e != nil {
			h.writeError(w, r, 503, "comfy_unavailable")
		} else {
			h.writeJSON(w, 200, map[string]any{"version": 1, "jobs": jobs})
		}
		return true
	}
	if rel == "/jobs" && r.Method == "POST" {
		if !h.authorizedMutation(r) {
			h.writeError(w, r, 403, "request_denied")
			return true
		}
		if r.Header.Get("Content-Type") != "application/json" {
			h.writeError(w, r, 400, "invalid_request")
			return true
		}
		var input struct {
			Key      string          `json:"key"`
			Workflow json.RawMessage `json:"workflow"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			h.writeError(w, r, 400, "invalid_request")
			return true
		}
		workflow, e := comfyui.ParseWorkflow(input.Workflow)
		if e != nil {
			h.writeError(w, r, 400, "invalid_workflow")
			return true
		}
		job, e := h.comfy.Submit(ctx, input.Key, workflow)
		// Return a known saved state even when remote delivery is uncertain. The UI
		// must observe this exact key, never generate another automatic submission.
		if job.State != "" {
			h.writeJSON(w, 200, map[string]any{"version": 1, "job": job})
			return true
		}
		if e != nil {
			h.writeError(w, r, 409, "job_conflict")
			return true
		}
		h.writeError(w, r, 503, "comfy_unavailable")
		return true
	}
	if len(parts) >= 2 && parts[0] == "jobs" {
		key := parts[1]
		if len(parts) == 2 && r.Method == "GET" {
			job, e := h.comfy.Status(ctx, key)
			if e != nil {
				h.writeError(w, r, 404, "job_unavailable")
			} else {
				h.writeJSON(w, 200, map[string]any{"version": 1, "job": job})
			}
			return true
		}
		if len(parts) == 3 && parts[2] == "cancel" && r.Method == "POST" {
			if !h.authorizedMutation(r) {
				h.writeError(w, r, 403, "request_denied")
				return true
			}
			if r.ContentLength != 0 {
				h.writeError(w, r, 400, "invalid_request")
				return true
			}
			if h.comfy.CancelPending(ctx, key) != nil {
				h.writeError(w, r, 409, "cancel_unconfirmed")
			} else {
				h.writeJSON(w, 200, map[string]any{"version": 1, "state": "pending_delete_requested"})
			}
			return true
		}
		if len(parts) == 3 && parts[2] == "image" && r.Method == "GET" {
			q := r.URL.Query()
			i, e := strconv.Atoi(q.Get("image"))
			if e != nil || i < 0 || len(q) != 2 || len(q["node"]) != 1 || len(q["image"]) != 1 {
				h.writeError(w, r, 400, "invalid_request")
				return true
			}
			b, e := h.comfy.Output(ctx, key, q.Get("node"), i)
			if e != nil {
				h.writeError(w, r, 404, "output_unavailable")
				return true
			}
			kind := http.DetectContentType(b)
			if kind != "image/png" && kind != "image/jpeg" && kind != "image/webp" {
				h.writeError(w, r, 422, "unsupported_image")
				return true
			}
			w.Header().Set("Content-Type", kind)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Disposition", "inline; filename=\"comfy-output\"")
			w.Write(b)
			return true
		}
	}
	h.writeError(w, r, 405, "invalid_action")
	return true
}
