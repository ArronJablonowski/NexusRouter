package app

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type capturingWorkboardEvaluator struct {
	request workboard.SubmitCandidateRequest
	actor   workboard.Actor
}

func (e *capturingWorkboardEvaluator) EvaluateCandidate(_ context.Context, request workboard.SubmitCandidateRequest, actor workboard.Actor) ([]workboard.EvidenceInput, error) {
	e.request = request
	e.request.ArtifactRefs = append([]string{}, request.ArtifactRefs...)
	e.actor = actor
	return []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "focused-test-report"}}, nil
}

func TestWorkboardWorkerDispatchOwnsOnlyWorkerLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "workboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Now().UTC().Add(-time.Second)
	bridge, err := NewWorkboardBridge(store, store, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	boardTitle := "Dispatch"
	board, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
		IdempotencyKey: "dispatch-board-key-01", Title: &boardTitle})
	if err != nil {
		t.Fatal(err)
	}
	cardTitle := "Worker task"
	criteria := []webui.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Tests pass", Required: true}}
	card, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCreate,
		IdempotencyKey: "dispatch-card-key-001", BoardID: board.BoardID, Title: &cardTitle, Criteria: criteria,
		ExpectedBoardRevision: revisionPointer(board.BoardRevision), ExpectedGraphRevision: revisionPointer(1)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardMove,
		IdempotencyKey: "dispatch-ready-key-01", BoardID: board.BoardID, CardID: card.CardID, TargetState: "ready",
		ExpectedBoardRevision: revisionPointer(snapshot.Board.Revision), ExpectedLayoutRevision: revisionPointer(snapshot.Board.LayoutRevision),
		ExpectedCardRevision: card.CardRevision})
	if err != nil {
		t.Fatal(err)
	}
	clock = time.Now().UTC().Add(time.Second)
	evaluator := &capturingWorkboardEvaluator{}
	dispatch, err := NewWorkboardWorkerDispatch(store, "worker-a", strings.Repeat("a", 64), time.Minute, evaluator, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Append(ctx, 0, runtime.Event{Version: 1, ID: "dispatch-start", TaskID: "dispatch-task", SessionID: "dispatch-session",
		CorrelationID: "dispatch-task", Sequence: 1, Time: clock, Kind: runtime.TaskStarted}); err != nil {
		t.Fatal(err)
	}
	claim, err := dispatch.Claim(ctx, workboard.ClaimRequest{BoardID: board.BoardID, CardID: card.CardID,
		IdempotencyKey: "dispatch-claim-key-01", ExpectedCardRevision: *ready.CardRevision,
		TaskID: "dispatch-task", SessionID: "dispatch-session"})
	if err != nil || claim.Validate() != nil || claim.ClaimRevision == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	claimed, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || claimed.Cards[0].CurrentAttemptID == "" || claimed.Cards[0].CurrentClaimID == "" {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	current := claimed.Cards[0]
	clock = clock.Add(time.Second)
	heartbeat, err := dispatch.Heartbeat(ctx, workboard.HeartbeatRequest{BoardID: board.BoardID, CardID: current.ID,
		AttemptID: current.CurrentAttemptID, ClaimID: current.CurrentClaimID, IdempotencyKey: "dispatch-heartbeat-01", ExpectedClaimRevision: *claim.ClaimRevision})
	if err != nil || heartbeat.Validate() != nil || heartbeat.ClaimRevision == nil {
		t.Fatalf("heartbeat=%+v err=%v", heartbeat, err)
	}
	clock = clock.Add(time.Second)
	blocked, err := dispatch.Block(ctx, workboard.ClaimCardControl{BoardID: board.BoardID, CardID: current.ID,
		AttemptID: current.CurrentAttemptID, ClaimID: current.CurrentClaimID, IdempotencyKey: "dispatch-block-key-01",
		ExpectedCardRevision: current.Revision, ExpectedClaimRevision: *heartbeat.ClaimRevision, ReasonCode: "provider-pressure"})
	if err != nil || blocked.Validate() != nil || blocked.CardRevision == nil {
		t.Fatalf("blocked=%+v err=%v", blocked, err)
	}
	clock = clock.Add(time.Second)
	unblocked, err := dispatch.Unblock(ctx, workboard.ClaimCardControl{BoardID: board.BoardID, CardID: current.ID,
		AttemptID: current.CurrentAttemptID, ClaimID: current.CurrentClaimID, IdempotencyKey: "dispatch-unblock-key-1",
		ExpectedCardRevision: *blocked.CardRevision, ExpectedClaimRevision: *heartbeat.ClaimRevision, ReasonCode: "provider-pressure"})
	if err != nil || unblocked.Validate() != nil || unblocked.CardRevision == nil {
		t.Fatalf("unblocked=%+v err=%v", unblocked, err)
	}
	clock = clock.Add(time.Second)
	checkpoint, err := dispatch.AppendCheckpoint(ctx, workboard.AppendCheckpointRequest{BoardID: board.BoardID, CardID: current.ID,
		AttemptID: current.CurrentAttemptID, ClaimID: current.CurrentClaimID, IdempotencyKey: "dispatch-checkpoint-1",
		ExpectedCardRevision: *unblocked.CardRevision, ExpectedClaimRevision: *heartbeat.ClaimRevision,
		CriteriaRevision: current.CriteriaRevision, Evidence: "Focused tests passed."})
	if err != nil || checkpoint.Validate() != nil {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
	projected, err := bridge.BrowserRead(ctx, strings.Repeat("b", 64), board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || projected.Validate() != nil || len(projected.Lifecycle) != 1 {
		t.Fatalf("lifecycle projection=%+v err=%v", projected.Lifecycle, err)
	}
	lifecycle := projected.Lifecycle[0]
	if lifecycle.CardID != current.ID || lifecycle.Attempt.ID != current.CurrentAttemptID || lifecycle.Attempt.Claim == nil ||
		lifecycle.Attempt.Claim.ID != current.CurrentClaimID || lifecycle.Attempt.Claim.Revision != *heartbeat.ClaimRevision ||
		lifecycle.CheckpointCount != 1 || lifecycle.CheckpointsHasMore || len(lifecycle.Checkpoints) != 1 || lifecycle.Checkpoints[0].Evidence != "Focused tests passed." {
		t.Fatalf("incorrect lifecycle projection: %+v", lifecycle)
	}
	latest := projected.Cards[0]
	claimRevision := lifecycle.Attempt.Claim.Revision
	clock = clock.Add(time.Second)
	summary := "Implemented the bounded worker path without losing candidate content."
	artifacts := []string{"artifact-one", "artifact-two"}
	submitted, err := dispatch.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: board.BoardID, CardID: latest.ID,
		AttemptID: latest.CurrentAttemptID, ClaimID: latest.CurrentClaimID, IdempotencyKey: "dispatch-candidate-001",
		ExpectedCardRevision: latest.Revision, ExpectedClaimRevision: claimRevision, CriteriaRevision: latest.CriteriaRevision,
		Summary: summary, ArtifactRefs: artifacts})
	if err != nil || submitted.Validate() != nil || evaluator.actor.ID != "worker-a" || evaluator.actor.Type != "worker" ||
		evaluator.request.Summary != summary || !reflect.DeepEqual(evaluator.request.ArtifactRefs, artifacts) {
		t.Fatalf("candidate=%+v evaluator=%+v/%+v err=%v", submitted, evaluator.actor, evaluator.request, err)
	}
	review, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || len(review.Lifecycle) != 1 || review.Lifecycle[0].Attempt.Candidate == nil || len(review.Lifecycle[0].Attempt.Evidence) != 1 {
		t.Fatalf("candidate lifecycle=%+v err=%v", review.Lifecycle, err)
	}
	candidate := review.Lifecycle[0].Attempt.Candidate
	evidence := review.Lifecycle[0].Attempt.Evidence
	clock = clock.Add(time.Second)
	accepted, err := bridge.BrowserMutate(ctx, strings.Repeat("b", 64), webui.BoardRequest{Version: 1, Action: webui.AcceptanceAccept,
		IdempotencyKey: "dispatch-accept-key-01", BoardID: board.BoardID, CardID: latest.ID, AttemptID: latest.CurrentAttemptID,
		CandidateID: candidate.ID, ExpectedCardRevision: revisionPointer(review.Cards[0].Revision), CriteriaRevision: revisionPointer(latest.CriteriaRevision),
		EvidenceHeadRevision: revisionPointer(evidence[len(evidence)-1].Revision), CandidateDigest: candidate.Digest,
		CriteriaDigest: candidate.CriteriaDigest, EvidenceSetDigest: workboard.EvidenceSetDigest([]workboard.EvidenceRecord{{
			Version: 1, ID: evidence[0].ID, Revision: evidence[0].Revision, BoardID: evidence[0].BoardID, CardID: evidence[0].CardID,
			AttemptID: evidence[0].AttemptID, CandidateID: evidence[0].CandidateID, CriterionID: evidence[0].CriterionID,
			Source: evidence[0].Source, Outcome: evidence[0].Outcome, ActorID: evidence[0].ActorID, ActorType: evidence[0].ActorType,
			Reference: evidence[0].Reference, CandidateDigest: evidence[0].CandidateDigest, CriteriaDigest: evidence[0].CriteriaDigest,
			PolicyDigest: evidence[0].PolicyDigest, CreatedAt: evidence[0].CreatedAt,
		}}), PolicyDigest: candidate.PolicyDigest, Evidence: "objective evidence accepted"})
	if err != nil || accepted.Validate() != nil {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	final, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || len(final.Lifecycle) != 1 || final.Lifecycle[0].Acceptance == nil ||
		final.Lifecycle[0].Acceptance.Rationale != "objective evidence accepted" ||
		final.Lifecycle[0].Acceptance.PriorEvidenceHeadRevision != evidence[len(evidence)-1].Revision {
		t.Fatalf("acceptance projection=%+v err=%v", final.Lifecycle, err)
	}
	history, err := bridge.NativeAttemptHistory(ctx, board.BoardID, current.ID, webui.AttemptHistoryOptions{Limit: 25})
	if err != nil || history.Validate() != nil || len(history.Items) != 1 || history.Items[0].ID != current.CurrentAttemptID || history.Items[0].State != "accepted" {
		t.Fatalf("attempt history=%+v err=%v", history, err)
	}
	detail, err := bridge.NativeAttemptDetail(ctx, board.BoardID, current.ID, current.CurrentAttemptID, webui.AttemptDetailOptions{Limit: 25})
	if err != nil || detail.Validate() != nil || detail.Attempt.AcceptanceID == "" || detail.Attempt.Candidate == nil || len(detail.Attempt.Evidence) != 1 {
		t.Fatalf("attempt detail=%+v err=%v", detail, err)
	}
}
