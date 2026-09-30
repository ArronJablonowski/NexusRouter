package submissions

import "github.com/ArronJablonowski/NexusRouter/runtime"

// StreamEvent assigns a durable submission-wide sequence to one immutable
// runtime event. Runtime event task-local identities remain unchanged.
type StreamEvent struct {
	Sequence int64         `json:"sequence"`
	Event    runtime.Event `json:"event"`
}

// StreamPage is one atomic observation of submission status and its globally
// ordered committed events. ResultSequence is zero until terminal; once set it
// is the virtual durable result marker immediately after EventHeadSequence.
type StreamPage struct {
	Version           int           `json:"version"`
	SubmissionID      string        `json:"submission_id"`
	FromSequence      int64         `json:"from_sequence"`
	NextSequence      int64         `json:"next_sequence"`
	EventHeadSequence int64         `json:"event_head_sequence"`
	ResultSequence    int64         `json:"result_sequence"`
	HasMoreEvents     bool          `json:"has_more_events"`
	Events            []StreamEvent `json:"events"`
	Status            Status        `json:"status"`
}
