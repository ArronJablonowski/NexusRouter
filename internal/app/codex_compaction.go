package app

import "github.com/ArronJablonowski/NexusRouter/sessions"

// This is a binding check on the private continuation prepared by the host,
// not independent proof of source accuracy or operator approval. Public inputs
// cannot supply continuationContext. loadContinuation verifies the original
// journal and reviewed proposal; the TaskStarted transaction rechecks approval.
func codexCompactionReady(r Request) bool {
	if r.Compaction == nil && r.SummaryAttemptID == "" {
		return r.continuation == nil || r.continuation.Compaction == nil
	}
	if r.ContinueTaskID == "" || r.continuation == nil || r.continuation.Compaction == nil || r.continuation.Privacy != "cloud_allowed" {
		return false
	}
	checkpoint := r.continuation.Compaction
	if checkpoint.Validate(r.ContinueTaskID) != nil {
		return false
	}
	if r.SummaryAttemptID != "" {
		return r.Compaction == nil && checkpoint.SummaryAttemptID == r.SummaryAttemptID && checkpoint.SummaryReviewID != ""
	}
	return sessions.ValidateCompactionRequest(r.Compaction) == nil && checkpoint.SummaryAttemptID == "" && checkpoint.SummaryReviewID == ""
}
