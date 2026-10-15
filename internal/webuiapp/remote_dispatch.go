package webuiapp

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"net/http"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// RemoteDispatcher must durably bind the request identity before network I/O.
// Production wiring uses RecordedRemoteDispatcher, never Client.Dispatch.
type RemoteDispatcher interface {
	Dispatch(context.Context, string, string, remote.Task) (submissions.Status, error)
}
type RecordedRemoteDispatcher struct {
	Client          *remote.Client
	Store           *remote.RouteStore
	UploadDirectory string
	PrepareUploads  func(context.Context, string, []attachments.File) (string, error)
}

func (d *RecordedRemoteDispatcher) Dispatch(ctx context.Context, instance, key string, task remote.Task) (submissions.Status, error) {
	if d == nil || d.Client == nil || d.Store == nil {
		return submissions.Status{}, remote.ErrInvalid
	}
	return d.DispatchFiles(ctx, instance, key, task, nil)
}

type remoteDispatchRequest struct {
	Version         int                `json:"version"`
	Instance        string             `json:"instance"`
	RequestID       string             `json:"request_id"`
	ParentRequestID string             `json:"parent_request_id,omitempty"`
	Task            remote.Task        `json:"task"`
	Files           []attachments.File `json:"files,omitempty"`
}

func (h *Handler) serveRemoteDispatch(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != h.basePath+"/api/v1/remote-dispatch" {
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
	var input remoteDispatchRequest
	if decodeMutationJSON(r, &input, 12<<20) != nil || input.Version != 1 || !remoteControlID(input.Instance, 1) || !remoteControlID(input.RequestID, 16) || !remoteTaskFits(input.Task) || attachments.Validate(input.Files) != nil || (input.ParentRequestID != "" && !remoteControlID(input.ParentRequestID, 16)) {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request")
		return true
	}
	if h.remoteDispatcher == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "dispatch_disabled")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	s, err := safeCall(func() (submissions.Status, error) {
		if input.ParentRequestID != "" {
			d, ok := h.remoteDispatcher.(*RecordedRemoteDispatcher)
			if !ok {
				return submissions.Status{}, errRemoteUpload
			}
			return d.DispatchContinuation(ctx, input.Instance, input.RequestID, input.ParentRequestID, input.Task, input.Files)
		}
		if len(input.Files) > 0 {
			d, ok := h.remoteDispatcher.(remoteFileDispatcher)
			if !ok {
				return submissions.Status{}, errRemoteUpload
			}
			return d.DispatchFiles(ctx, input.Instance, input.RequestID, input.Task, input.Files)
		}
		return h.remoteDispatcher.Dispatch(ctx, input.Instance, input.RequestID, input.Task)
	})
	if errors.Is(err, errRemoteUpload) {
		h.writeError(w, r, 422, "upload_rejected")
		return true
	}
	// Once dispatch was called, any failure is conservatively uncertain. Do not
	// erase durable intent, generate a new key, retry or expose remote diagnostics.
	if err != nil || !validRemoteControlStatus(s) {
		h.writeError(w, r, http.StatusServiceUnavailable, "dispatch_unconfirmed")
		return true
	}
	h.writeJSON(w, http.StatusOK, remoteTaskControlPage{Version: 1, Instance: input.Instance, RequestID: input.RequestID, SubmissionID: s.ID, State: s.State, TaskIDs: s.TaskIDs, CancelRequested: s.CancelRequested, ObservedAt: time.Now().UTC()})
	return true
}
