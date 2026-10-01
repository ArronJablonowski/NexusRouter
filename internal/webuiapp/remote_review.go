package webuiapp

import (
	"context"
	"net/http"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

// RemoteAutomaticReviewer authenticates original intent and canonical completion.
// Evaluator policy is host configuration, never a browser-supplied verdict.
type RemoteAutomaticReviewer interface {
	Evaluate(context.Context, string, remote.AutomaticRequest) (remote.RemoteEvaluationStatus, error)
	InspectReview(context.Context, string, remote.AutomaticRequest) (remote.AutomaticReviewState, error)
}
type RecordedRemoteReviewer struct {
	Client       *remote.Client
	Store        *remote.RouteStore
	EvidenceRoot string
	Policy       func(bool) (remote.RemoteEvaluator, error)
}

func (d *RecordedRemoteReviewer) Evaluate(ctx context.Context, key string, request remote.AutomaticRequest) (remote.RemoteEvaluationStatus, error) {
	if d == nil || d.Client == nil || d.Store == nil || d.Policy == nil {
		return remote.RemoteEvaluationStatus{}, remote.ErrInvalid
	}
	task, _, err := d.Store.ResolveAutomatic(key, request)
	if err != nil {
		return remote.RemoteEvaluationStatus{}, err
	}
	policy, err := d.Policy(task.Private)
	if err != nil {
		return remote.RemoteEvaluationStatus{}, err
	}
	if err = remote.PrepareOutcomeEvidence(d.EvidenceRoot); err != nil {
		return remote.RemoteEvaluationStatus{}, err
	}
	return d.Client.EvaluateRecordedOutcome(ctx, d.Store, d.EvidenceRoot, key, task, policy)
}
func (d *RecordedRemoteReviewer) InspectReview(ctx context.Context, key string, request remote.AutomaticRequest) (remote.AutomaticReviewState, error) {
	if d == nil || d.Client == nil || d.Store == nil {
		return remote.AutomaticReviewState{}, remote.ErrInvalid
	}
	return d.Client.InspectAutomaticReview(ctx, d.Store, d.EvidenceRoot, key, request)
}

type remoteReviewPage struct {
	Version        int    `json:"version"`
	RequestID      string `json:"request_id"`
	ReceiptSHA256  string `json:"receipt_sha256,omitempty"`
	Status         string `json:"status"`
	ReviewApplied  bool   `json:"review_applied"`
	Classification string `json:"classification,omitempty"`
	Verdict        string `json:"verdict,omitempty"`
	Method         string `json:"method,omitempty"`
}

func (h *Handler) serveRemoteReview(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != h.basePath+"/api/v1/remote-auto-review" {
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
	var input struct {
		Action  string                 `json:"action"`
		Request remoteAutomaticRequest `json:"request"`
	}
	if decodeMutationJSON(r, &input, remote.MaxBody) != nil || (input.Action != "evaluate" && input.Action != "status") {
		h.writeError(w, r, 400, "invalid_request")
		return true
	}
	request, err := input.Request.request()
	if err != nil {
		h.writeError(w, r, 400, "invalid_request")
		return true
	}
	if h.remoteReviewer == nil {
		h.writeError(w, r, 503, "review_disabled")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	page := remoteReviewPage{Version: 1, RequestID: input.Request.RequestID}
	if input.Action == "evaluate" {
		result, e := safeCall(func() (remote.RemoteEvaluationStatus, error) {
			return h.remoteReviewer.Evaluate(ctx, input.Request.RequestID, request)
		})
		if e != nil || result.Version != 1 {
			h.writeError(w, r, 503, "review_unconfirmed")
			return true
		}
		page.ReceiptSHA256 = result.ReceiptSHA256
		page.Status = result.Status
		page.ReviewApplied = result.ReviewApplied
		// Do not project result.Review: a newer operator head may already exist.
	} else {
		result, e := safeCall(func() (remote.AutomaticReviewState, error) {
			return h.remoteReviewer.InspectReview(ctx, input.Request.RequestID, request)
		})
		if e != nil || result.Version != 1 {
			h.writeError(w, r, 503, "review_status_unavailable")
			return true
		}
		page.ReceiptSHA256 = result.ReceiptSHA256
		page.Status = "unrecorded"
		if result.Recorded && result.Evidence != nil {
			page.Status = "recorded"
			page.Classification = result.Evidence.Classification
			if head := result.Evidence.Head; head != nil {
				page.Verdict = head.Verdict
				page.Method = head.Method
			}
		}
	}
	h.writeJSON(w, 200, page)
	return true
}
