package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/daemon"
)

func (h *Handler) serveDaemonControl(w http.ResponseWriter, r *http.Request) {
	stop := r.URL.Path == "/v1/daemon/stop"
	method := http.MethodGet
	if stop {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		failure(w, 405, "method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.TransferEncoding) != 0 || r.ContentLength < 0 {
		failure(w, 400, "invalid_request")
		return
	}
	if !stop && (r.ContentLength != 0 || (r.Body != nil && r.Body != http.NoBody)) {
		failure(w, 400, "invalid_request")
		return
	}
	if (stop && h.services.StopDaemon == nil) || (!stop && h.services.DaemonStatus == nil) {
		failure(w, 503, "daemon_unavailable")
		return
	}
	select {
	case h.controls <- struct{}{}:
		defer func() { <-h.controls }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "control_capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 503, "daemon_unavailable")
		return
	}
	id := ""
	if stop {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			failure(w, 415, "json_required")
			return
		}
		if r.Body == nil || r.ContentLength > 1024 {
			failure(w, 413, "invalid_request")
			return
		}
		// A real HTTP connection receives a matching deadline; custom bodies
		// must cooperate, as elsewhere in this server's bounded control API.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second))
		defer http.NewResponseController(w).SetReadDeadline(time.Time{})
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil {
			failure(w, 400, "invalid_request")
			return
		}
		fields, err := chatObject(body, "instance_id")
		if err != nil || len(fields) != 1 || json.Unmarshal(fields["instance_id"], &id) != nil || !daemon.ValidInstanceID(id) {
			failure(w, 400, "invalid_request")
			return
		}
	}
	if ctx.Err() != nil {
		failure(w, 503, "daemon_unavailable")
		return
	}
	status, err := h.callDaemonControl(ctx, stop, id)
	if !stop && ctx.Err() != nil {
		failure(w, 503, "daemon_unavailable")
		return
	}
	if err != nil {
		if errors.Is(err, daemon.ErrInstance) {
			failure(w, 409, "daemon_instance_mismatch")
		} else {
			failure(w, 503, "daemon_unavailable")
		}
		return
	}
	// A successful Stop may itself cancel the daemon lifetime. Preserve its
	// acknowledgement; HTTP graceful shutdown waits for this in-flight handler.
	if status.Validate() != nil || (stop && (status.InstanceID != id || status.State != "stopping")) {
		failure(w, 500, "invalid_daemon_status")
		return
	}
	writeJSON(w, 200, status)
}

func (h *Handler) callDaemonControl(ctx context.Context, stop bool, id string) (status daemon.Status, err error) {
	defer func() {
		if recover() != nil {
			status = daemon.Status{}
			err = daemon.ErrControl
		}
	}()
	if stop {
		return h.services.StopDaemon(ctx, id)
	}
	return h.services.DaemonStatus(ctx)
}
