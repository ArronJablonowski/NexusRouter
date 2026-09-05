package submissions

import "time"

type Recovery struct {
	Version      int       `json:"version"`
	ID           string    `json:"id"`
	SubmissionID string    `json:"submission_id"`
	Time         time.Time `json:"time"`
	Action       string    `json:"action"`
	Reason       string    `json:"reason"`
}

func (r Recovery) Validate() error {
	if r.Version != 1 || !validID(r.ID) || !validID(r.SubmissionID) || r.Time.IsZero() {
		return ErrInvalid
	}
	if (r.Action == "queued" && r.Reason == "lease_expired_no_task") || (r.Action == "canceled" && r.Reason == "cancellation_requested") || (r.Action == "failed" && r.Reason == "recovery_limit") || (r.Reason == "terminal_history" && (r.Action == "succeeded" || r.Action == "failed" || r.Action == "canceled")) {
		return nil
	}
	if (r.Reason == "interrupted_delegation" || r.Reason == "interrupted_model") && (r.Action == "failed" || r.Action == "canceled") {
		return nil
	}
	return ErrInvalid
}
