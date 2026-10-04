package webuiapp

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"net/http"
	"time"
)

func (h *Handler) serveRemoteRunner(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != h.basePath+"/api/v1/remote-runner" {
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
		h.writeError(w, r, 503, "mutation_capacity")
		return true
	}
	defer releaseMutationSlot(h, false)
	var input struct {
		Version  int    `json:"version"`
		Instance string `json:"instance"`
		Action   string `json:"action"`
	}
	if decodeMutationJSON(r, &input, 4096) != nil || input.Version != 1 || input.Instance == "" || len(input.Instance) > 64 || (input.Action != "status" && input.Action != "start" && input.Action != "stop") {
		h.writeError(w, r, 400, "invalid_request")
		return true
	}
	client, ok := h.remoteInspector.(interface {
		Runner(context.Context, string, string) (remote.RunnerStatus, error)
	})
	if !ok {
		h.writeError(w, r, 503, "runner_unavailable")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := safeCall(func() (remote.RunnerStatus, error) { return client.Runner(ctx, input.Instance, input.Action) })
	if err != nil || out.Validate() != nil {
		h.writeError(w, r, 409, "runner_unavailable_or_busy")
		return true
	}
	h.writeJSON(w, http.StatusOK, out)
	return true
}
