package webuiapp

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"net/http"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/scheduleview"
	"github.com/ArronJablonowski/NexusRouter/remote"
)

// RemoteInspector is bound by the host to fixed credentials and a trust file.
// Browser requests select only a configured instance, never URLs or credentials.
type RemoteInspector interface {
	Info(context.Context, string) (remote.Info, error)
	Tasks(context.Context, string, string) (remote.TaskPage, error)
}
type remoteInspectionRequest struct {
	Version  int    `json:"version"`
	Instance string `json:"instance"`
	View     string `json:"view"`
	After    string `json:"after,omitempty"`
}
type remoteInspectionPage struct {
	Dependencies *webui.DependencyInventory `json:"dependencies,omitempty"`
	OSSchedules  *scheduleview.Page         `json:"os_schedules,omitempty"`
	Version      int                        `json:"version"`
	ObservedAt   time.Time                  `json:"observed_at"`
	Info         *remote.Info               `json:"info,omitempty"`
	Tasks        *remote.TaskPage           `json:"tasks,omitempty"`
}

func (h *Handler) serveRemoteInspection(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != h.basePath+"/api/v1/remote-inspection" {
		return false
	}
	if r.Method != http.MethodPost {
		h.authenticatedAPINotFound(w, r)
		return true
	}
	if !h.requireMutationAuthority(w, r) {
		return true
	}
	if !mutationSlot(h, false) {
		h.writeError(w, r, http.StatusServiceUnavailable, "mutation_capacity")
		return true
	}
	defer releaseMutationSlot(h, false)
	var input remoteInspectionRequest
	if decodeMutationJSON(r, &input, 4096) != nil || input.Version != 1 || input.Instance == "" || len(input.Instance) > 64 || (input.View != "info" && input.View != "tasks" && input.View != "os_schedules" && input.View != "dependencies") || (input.View != "tasks" && input.After != "") || len(input.After) > 64 {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request")
		return true
	}
	if h.remoteInspector == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "inspection_unavailable")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	page := remoteInspectionPage{Version: 1}
	var err error
	if input.View == "dependencies" {
		reader, ok := h.remoteInspector.(interface {
			Dependencies(context.Context, string) (webui.DependencyInventory, error)
		})
		if !ok {
			err = remote.ErrUnavailable
		} else {
			var result webui.DependencyInventory
			result, err = reader.Dependencies(ctx, input.Instance)
			if err == nil {
				err = result.Validate()
			}
			page.Dependencies = &result
		}
	} else if input.View == "os_schedules" {
		reader, ok := h.remoteInspector.(interface {
			OSSchedules(context.Context, string) (scheduleview.Page, error)
		})
		if !ok {
			err = remote.ErrUnavailable
		} else {
			var result scheduleview.Page
			result, err = safeCall(func() (scheduleview.Page, error) { return reader.OSSchedules(ctx, input.Instance) })
			if err == nil {
				err = result.Validate()
			}
			page.OSSchedules = &result
		}
	} else if input.View == "info" {
		var info remote.Info
		info, err = safeCall(func() (remote.Info, error) { return h.remoteInspector.Info(ctx, input.Instance) })
		if err == nil && (info.ValidateRouting() != nil || info.Version != 1 || info.Instance != input.Instance || len(info.Models) > 4096 || len(info.Harnesses) > 256) {
			err = remote.ErrInvalid
		}
		page.Info = &info
	} else {
		var tasks remote.TaskPage
		tasks, err = safeCall(func() (remote.TaskPage, error) { return h.remoteInspector.Tasks(ctx, input.Instance, input.After) })
		if err == nil && (tasks.Version != 1 || tasks.Instance != input.Instance || tasks.After != input.After || len(tasks.Tasks) > 100) {
			err = remote.ErrInvalid
		}
		page.Tasks = &tasks
	}
	if err != nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "inspection_unavailable")
		return true
	}
	page.ObservedAt = time.Now().UTC()
	h.writeJSON(w, http.StatusOK, page)
	return true
}
