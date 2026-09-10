package workboard

import "context"

const WorkerControlObservationVersion = 1

// WorkerControlTarget is the exact durable identity a running worker is
// permitted to observe. Revisions are fences: a control decision must never be
// applied to a replacement attempt, claim, or worker after reassignment.
type WorkerControlTarget struct {
	BoardID, CardID, AttemptID, ClaimID, WorkerID, TaskID string
	CardRevision, ClaimRevision                           int64
}

func (t WorkerControlTarget) Validate() error {
	if !validLifecycleIDs(t.BoardID, t.CardID, t.AttemptID, t.ClaimID, t.WorkerID, t.TaskID) ||
		t.CardRevision < 1 || t.ClaimRevision < 1 {
		return fail(CodeInvalid, "worker_control_target")
	}
	return nil
}

// WorkerControlObservation is a coherent, read-only projection of operator
// control flags for one exact running claim. Pause is projected for visibility
// only; worker pause semantics require a separate acknowledgement protocol.
type WorkerControlObservation struct {
	Version                                               int
	BoardID, CardID, AttemptID, ClaimID, WorkerID, TaskID string
	CardRevision, ClaimRevision                           int64
	CancelRequested, PauseRequested                       bool
}

func (o WorkerControlObservation) Validate(target WorkerControlTarget) error {
	if target.Validate() != nil || o.Version != WorkerControlObservationVersion ||
		o.BoardID != target.BoardID || o.CardID != target.CardID || o.AttemptID != target.AttemptID ||
		o.ClaimID != target.ClaimID || o.WorkerID != target.WorkerID || o.TaskID != target.TaskID ||
		o.CardRevision != target.CardRevision || o.ClaimRevision != target.ClaimRevision {
		return fail(CodeInvalid, "worker_control_observation")
	}
	return nil
}

// WorkerControlRepository reads operator control flags and all binding fences
// from one durable snapshot. Implementations must not synthesize control state
// from process-local signals.
type WorkerControlRepository interface {
	ReadWorkerControl(context.Context, WorkerControlTarget) (WorkerControlObservation, error)
}
