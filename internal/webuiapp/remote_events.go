package webuiapp

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type remoteEventReader interface {
	Events(context.Context, string, string, string, int64) (sessions.EventPage, error)
}
type remoteEventsRequest struct {
	Version   int    `json:"version"`
	Instance  string `json:"instance"`
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id"`
	After     int64  `json:"after"`
}
type remoteEventSummary struct {
	Sequence int64     `json:"sequence"`
	Kind     string    `json:"kind"`
	Time     time.Time `json:"time"`
}
type remoteEventsPage struct {
	Version   int                  `json:"version"`
	Instance  string               `json:"instance"`
	RequestID string               `json:"request_id"`
	TaskID    string               `json:"task_id"`
	State     string               `json:"state"`
	From      int64                `json:"from_sequence"`
	Next      int64                `json:"next_sequence"`
	Head      int64                `json:"head_sequence"`
	HasMore   bool                 `json:"has_more"`
	Events    []remoteEventSummary `json:"events"`
}

func (h *Handler) serveRemoteEvents(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != h.basePath+"/api/v1/remote-task-events" {
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
	var input remoteEventsRequest
	if decodeMutationJSON(r, &input, 4096) != nil || input.Version != 1 || !remoteControlID(input.Instance, 1) || !remoteControlID(input.RequestID, 16) || !sessions.ValidEventPageID(input.TaskID) || input.After < 0 || input.After > sessions.MaxTaskEvents {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request")
		return true
	}
	reader, ok := h.remoteTaskController.(remoteEventReader)
	if !ok {
		h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	status, err := safeCall(func() (bool, error) {
		s, e := h.remoteTaskController.Status(ctx, input.Instance, input.RequestID)
		return validRemoteControlStatus(s) && slices.Contains(s.TaskIDs, input.TaskID), e
	})
	if err != nil || !status {
		h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable")
		return true
	}
	page, err := safeCall(func() (sessions.EventPage, error) {
		return reader.Events(ctx, input.Instance, input.RequestID, input.TaskID, input.After)
	})
	if err != nil || page.Validate() != nil || page.TaskID != input.TaskID || page.FromSequence != input.After || page.HeadSequence > sessions.MaxTaskEvents {
		h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable")
		return true
	}
	// Only lifecycle facts reach this browser projection. Raw runtime payloads
	// can contain prompts, tool arguments, configuration and session data.
	out := remoteEventsPage{Version: 1, Instance: input.Instance, RequestID: input.RequestID, TaskID: page.TaskID, State: page.State, From: page.FromSequence, Next: page.NextSequence, Head: page.HeadSequence, HasMore: page.HasMore, Events: make([]remoteEventSummary, 0, len(page.Events))}
	for _, e := range page.Events {
		out.Events = append(out.Events, remoteEventSummary{Sequence: e.Sequence, Kind: string(e.Kind), Time: e.Time})
	}
	h.writeJSON(w, http.StatusOK, out)
	return true
}
