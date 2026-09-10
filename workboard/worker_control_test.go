package workboard

import "testing"

func TestWorkerControlObservationRequiresExactTarget(t *testing.T) {
	target := WorkerControlTarget{BoardID: "board", CardID: "card", AttemptID: "attempt", ClaimID: "claim",
		WorkerID: "worker", TaskID: "task", CardRevision: 4, ClaimRevision: 3}
	observation := WorkerControlObservation{Version: WorkerControlObservationVersion, BoardID: target.BoardID,
		CardID: target.CardID, AttemptID: target.AttemptID, ClaimID: target.ClaimID, WorkerID: target.WorkerID,
		TaskID: target.TaskID, CardRevision: target.CardRevision, ClaimRevision: target.ClaimRevision, CancelRequested: true}
	if target.Validate() != nil || observation.Validate(target) != nil {
		t.Fatal("valid worker control observation rejected")
	}
	for _, mutate := range []func(*WorkerControlObservation){
		func(value *WorkerControlObservation) { value.Version++ },
		func(value *WorkerControlObservation) { value.CardID = "other-card" },
		func(value *WorkerControlObservation) { value.AttemptID = "other-attempt" },
		func(value *WorkerControlObservation) { value.ClaimID = "other-claim" },
		func(value *WorkerControlObservation) { value.WorkerID = "other-worker" },
		func(value *WorkerControlObservation) { value.TaskID = "other-task" },
		func(value *WorkerControlObservation) { value.CardRevision++ },
		func(value *WorkerControlObservation) { value.ClaimRevision++ },
	} {
		changed := observation
		mutate(&changed)
		if changed.Validate(target) == nil {
			t.Fatalf("mismatched observation accepted: %+v", changed)
		}
	}
	invalid := target
	invalid.ClaimRevision = 0
	if invalid.Validate() == nil || observation.Validate(invalid) == nil {
		t.Fatal("invalid target accepted")
	}
}
