package approvals

import "time"

// ExecutionStatus correlates approval, journal and writer observations in one
// read snapshot. It cannot prove a process is dead or grant permission to retry.
// RecordedEffect is the tool's durable report, not independent artifact proof.
type ExecutionStatus struct {
	Version          int       `json:"version"`
	Approval         Record    `json:"approval"`
	TaskState        string    `json:"task_state"`
	Sequence         int64     `json:"sequence"`
	CallState        string    `json:"call_state"`
	RecordedEffect   string    `json:"recorded_effect,omitempty"`
	ScopeWriterState string    `json:"scope_writer_state"`
	ObservedAt       time.Time `json:"observed_at"`
}

func (s ExecutionStatus) Validate() error {
	if s.Version != Version || s.Approval.Validate() != nil || s.Sequence < 1 || s.Sequence > 10000 || !validTime(s.ObservedAt) {
		return ErrInvalid
	}
	switch s.TaskState {
	case "running", "completed", "failed", "canceled":
	default:
		return ErrInvalid
	}
	switch s.ScopeWriterState {
	case "none", "live", "expired":
	default:
		return ErrInvalid
	}
	switch s.CallState {
	case "open":
		if s.RecordedEffect != "" || s.TaskState == "completed" {
			return ErrInvalid
		}
	case "completed":
		switch s.RecordedEffect {
		case "none", "confirmed", "uncertain":
		default:
			return ErrInvalid
		}
		if s.RecordedEffect == "uncertain" && s.TaskState == "completed" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
