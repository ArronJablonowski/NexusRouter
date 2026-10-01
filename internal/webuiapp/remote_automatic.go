package webuiapp

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/remote"
)

// RemoteAutomatic routes only through persisted caller-owned selection evidence.
// The browser cannot supply candidates, quality scores, paths or exploration.
type RemoteAutomatic interface {
	DispatchAutomatic(context.Context, string, remote.AutomaticRequest) (remote.RecordedRequestStatus, error)
	InspectRecorded(context.Context, string) (remote.RecordedRequestStatus, error)
}
type RecordedRemoteAutomatic struct {
	Client       *remote.Client
	Store        *remote.RouteStore
	EvidenceRoot string
	ReviewQueue  *remote.ReviewQueue
	ReviewWait   time.Duration
	ReviewPolicy func(bool) (remote.RemoteEvaluator, error)
}

func (d *RecordedRemoteAutomatic) BackgroundReviewEnabled() bool {
	return d != nil && d.ReviewQueue != nil
}

func (d *RecordedRemoteAutomatic) DispatchAutomatic(ctx context.Context, key string, request remote.AutomaticRequest) (remote.RecordedRequestStatus, error) {
	if d == nil || d.Client == nil || d.Store == nil || request.Routing.AllowExploration {
		return remote.RecordedRequestStatus{}, remote.ErrInvalid
	}
	if d.ReviewQueue != nil {
		if d.ReviewPolicy == nil || d.ReviewWait <= 0 || d.ReviewWait > 24*time.Hour {
			return remote.RecordedRequestStatus{}, remote.ErrInvalid
		}
		deadline, err := d.ReviewQueue.Deadline(key)
		if errors.Is(err, os.ErrNotExist) {
			deadline = time.Now().UTC().Add(d.ReviewWait)
		} else if err != nil {
			return remote.RecordedRequestStatus{}, err
		}
		reviewer, err := d.ReviewPolicy(request.Routing.LocalRequired)
		if err != nil {
			return remote.RecordedRequestStatus{}, err
		}
		status, choice, err := d.Client.DispatchQueuedAutomaticReview(ctx, d.ReviewQueue, d.Store, d.EvidenceRoot, key, request, reviewer, deadline)
		if err != nil {
			return remote.RecordedRequestStatus{}, err
		}
		return remote.RecordedRequestStatus{Version: 1, RequestID: key, Destination: choice.Destination, Status: status}, nil
	}
	if err := remote.PrepareOutcomeEvidence(d.EvidenceRoot); err != nil {
		return remote.RecordedRequestStatus{}, err
	}
	status, choice, err := d.Client.DispatchDiscovered(ctx, d.Store, d.EvidenceRoot, key, request, harness.DefaultPolicy(), 0)
	if err != nil {
		return remote.RecordedRequestStatus{}, err
	}
	return remote.RecordedRequestStatus{Version: 1, RequestID: key, Destination: choice.Destination, Status: status}, nil
}
func (d *RecordedRemoteAutomatic) InspectRecorded(ctx context.Context, key string) (remote.RecordedRequestStatus, error) {
	if d == nil || d.Client == nil || d.Store == nil {
		return remote.RecordedRequestStatus{}, remote.ErrInvalid
	}
	return d.Client.InspectRecorded(ctx, d.Store, key)
}

type remoteAutomaticRequest struct {
	Version       int      `json:"version"`
	RequestID     string   `json:"request_id"`
	Prompt        string   `json:"prompt"`
	Domain        string   `json:"domain"`
	Profile       string   `json:"profile"`
	Difficulty    string   `json:"difficulty"`
	ContextTokens int64    `json:"context_tokens"`
	MaxCost       float64  `json:"max_cost"`
	Private       bool     `json:"private"`
	Capabilities  []string `json:"capabilities"`
}

func (in remoteAutomaticRequest) request() (remote.AutomaticRequest, error) {
	request := remote.AutomaticRequest{Version: 1, Prompt: in.Prompt, Routing: harness.Request{Version: 1, Task: harness.TaskClass{Domain: in.Domain, Profile: in.Profile, Difficulty: in.Difficulty}, Mode: "hybrid", LocalRequired: in.Private, ContextTokens: in.ContextTokens, MaxCost: in.MaxCost, Capabilities: in.Capabilities}}
	if in.Version != 1 || !remoteControlID(in.RequestID, 16) || len(in.Prompt) == 0 || len(in.Prompt) > remote.MaxBody/2 || in.ContextTokens < 8192 || in.ContextTokens > 1<<24 {
		return request, remote.ErrInvalid
	}
	_, err := harness.SelectScoped(request.Routing, harness.DefaultPolicy(), nil, time.Now().UTC(), 0)
	if err != nil && !errors.Is(err, harness.ErrNoRoute) {
		return request, err
	}
	return request, nil
}
func (h *Handler) serveRemoteAutomatic(w http.ResponseWriter, r *http.Request) bool {
	dispatch := r.URL.Path == h.basePath+"/api/v1/remote-auto-dispatch"
	if !dispatch && r.URL.Path != h.basePath+"/api/v1/remote-recorded-status" {
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
		h.writeError(w, r, 503, "mutation_capacity")
		return true
	}
	defer releaseMutationSlot(h, true)
	var key string
	var request remote.AutomaticRequest
	if dispatch {
		var input remoteAutomaticRequest
		if decodeMutationJSON(r, &input, remote.MaxBody) != nil {
			h.writeError(w, r, 400, "invalid_request")
			return true
		}
		var err error
		request, err = input.request()
		key = input.RequestID
		if err != nil {
			h.writeError(w, r, 400, "invalid_request")
			return true
		}
	} else {
		var input struct {
			Version   int    `json:"version"`
			RequestID string `json:"request_id"`
		}
		if decodeMutationJSON(r, &input, 4096) != nil || input.Version != 1 || !remoteControlID(input.RequestID, 16) {
			h.writeError(w, r, 400, "invalid_request")
			return true
		}
		key = input.RequestID
	}
	if h.remoteAutomatic == nil {
		h.writeError(w, r, 503, "automatic_routing_disabled")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := safeCall(func() (remote.RecordedRequestStatus, error) {
		if dispatch {
			return h.remoteAutomatic.DispatchAutomatic(ctx, key, request)
		}
		return h.remoteAutomatic.InspectRecorded(ctx, key)
	})
	if err != nil || result.Version != 1 || result.RequestID != key || !remoteControlID(result.Destination, 1) || !validRemoteControlStatus(result.Status) {
		code := "status_unavailable"
		if dispatch {
			code = "dispatch_unconfirmed"
		}
		h.writeError(w, r, 503, code)
		return true
	}
	s := result.Status
	h.writeJSON(w, 200, remoteTaskControlPage{Version: 1, Instance: result.Destination, RequestID: key, SubmissionID: s.ID, State: s.State, TaskIDs: s.TaskIDs, CancelRequested: s.CancelRequested, ObservedAt: time.Now().UTC()})
	return true
}
