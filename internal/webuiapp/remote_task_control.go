package webuiapp

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type RemoteTaskController interface {
	Status(context.Context, string, string) (submissions.Status, error)
	Cancel(context.Context, string, string) (submissions.Status, error)
}
type remoteTaskControlRequest struct {
	Version              int    `json:"version"`
	Instance             string `json:"instance"`
	RequestID            string `json:"request_id"`
	Action               string `json:"action"`
	ExpectedSubmissionID string `json:"expected_submission_id,omitempty"`
}
type remoteTaskControlPage struct {
	Version         int       `json:"version"`
	Instance        string    `json:"instance"`
	RequestID       string    `json:"request_id"`
	SubmissionID    string    `json:"submission_id"`
	State           string    `json:"state"`
	TaskIDs         []string  `json:"task_ids"`
	CancelRequested bool      `json:"cancel_requested"`
	ResultText      string    `json:"result_text,omitempty"`
	ObservedAt      time.Time `json:"observed_at"`
}

func remoteControlID(value string, min int) bool {
	if len(value) < min || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func validRemoteControlStatus(s submissions.Status) bool {
	return s.Version == 1 && s.ID != "" && len(s.ID) <= 128 && len(s.TaskIDs) <= 128 && slices.Contains([]string{"queued", "running", "succeeded", "failed", "canceled"}, s.State)
}
func (h *Handler) serveRemoteTaskControl(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != h.basePath+"/api/v1/remote-task-control" {
		return false
	}
	if r.Method != http.MethodPost {
		h.authenticatedAPINotFound(w, r)
		return true
	}
	if !h.requireMutationAuthority(w, r) {
		return true
	}
	if !mutationSlot(h, true) {
		h.writeError(w, r, http.StatusServiceUnavailable, "mutation_capacity")
		return true
	}
	defer releaseMutationSlot(h, true)
	var input remoteTaskControlRequest
	if decodeMutationJSON(r, &input, 4096) != nil || input.Version != 1 || !remoteControlID(input.Instance, 1) || !remoteControlID(input.RequestID, 16) || (input.Action != "status" && input.Action != "cancel") || (input.Action == "status" && input.ExpectedSubmissionID != "") || (input.Action == "cancel" && (input.ExpectedSubmissionID == "" || len(input.ExpectedSubmissionID) > 128)) {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request")
		return true
	}
	if h.remoteTaskController == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "controls_unavailable")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	status, err := safeCall(func() (submissions.Status, error) {
		return h.remoteTaskController.Status(ctx, input.Instance, input.RequestID)
	})
	if err == nil && !validRemoteControlStatus(status) {
		err = remote.ErrInvalid
	}
	if err != nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "controls_unavailable")
		return true
	}
	if input.Action == "cancel" {
		if status.ID != input.ExpectedSubmissionID || !slices.Contains([]string{"queued", "running"}, status.State) {
			h.writeError(w, r, http.StatusConflict, "task_changed")
			return true
		}
		// No retry after an uncertain cancellation. The next action must inspect
		// the original request. Destination ownership and cancel scope are decisive.
		status, err = safeCall(func() (submissions.Status, error) {
			return h.remoteTaskController.Cancel(ctx, input.Instance, input.RequestID)
		})
		if err == nil && (!validRemoteControlStatus(status) || status.ID != input.ExpectedSubmissionID) {
			err = remote.ErrInvalid
		}
		if err != nil {
			h.writeError(w, r, http.StatusServiceUnavailable, "cancellation_unconfirmed")
			return true
		}
	}
	page := remoteTaskControlPage{Version: 1, Instance: input.Instance, RequestID: input.RequestID, SubmissionID: status.ID, State: status.State, TaskIDs: status.TaskIDs, CancelRequested: status.CancelRequested, ObservedAt: time.Now().UTC()}
	if status.State == "succeeded" && status.Result != nil {
		page.ResultText = status.Result.Text
	}
	h.writeJSON(w, http.StatusOK, page)
	return true
}
