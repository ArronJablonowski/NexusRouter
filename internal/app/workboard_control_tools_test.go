package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type unusedRecoveryVerifier struct{}

func (unusedRecoveryVerifier) VerifyRecovery(context.Context, workboard.RecoverClaimRequest, workboard.Actor) (workboard.RecoveryProof, error) {
	return workboard.RecoveryProof{}, nil
}

func TestRootAgentWorkboardControlRequestsAreApprovedScopedAndTaskAttributed(t *testing.T) {
	ctx := context.Background()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "workboards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bridge, err := NewWorkboardBridge(store, store, defaultWorkboardNow)
	if err != nil {
		t.Fatal(err)
	}
	title := "Agent controls"
	board, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: webui.ContractVersion, Action: webui.BoardCreate,
		IdempotencyKey: "control-board-key-01", Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	criterion := webui.AcceptanceCriterion{Version: webui.ContractVersion, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Tests pass", Required: true}
	card, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: webui.ContractVersion, Action: webui.CardCreate,
		IdempotencyKey: "control-card-key-001", BoardID: board.BoardID, Title: &title, Criteria: []webui.AcceptanceCriterion{criterion},
		ExpectedBoardRevision: &board.BoardRevision, ExpectedGraphRevision: revisionPointer(1)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	ready := "ready"
	moved, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: webui.ContractVersion, Action: webui.CardMove,
		IdempotencyKey: "control-ready-key-01", BoardID: board.BoardID, CardID: card.CardID, TargetState: ready,
		ExpectedBoardRevision: &snapshot.Board.Revision, ExpectedLayoutRevision: &snapshot.Board.LayoutRevision, ExpectedCardRevision: card.CardRevision})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := workboard.NewLifecycleService(store, contextWorkboardAuthority{}, unusedRecoveryVerifier{}, defaultWorkboardNow,
		time.Minute, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	workerCtx := context.WithValue(ctx, workboardAuthorityKey{}, workboard.Authority{CreationScope: "worker-scope", Actor: workboard.Actor{ID: "worker-a", Type: "worker"}})
	if _, err = lifecycle.Claim(workerCtx, workboard.ClaimRequest{BoardID: board.BoardID, CardID: card.CardID,
		IdempotencyKey: "control-claim-key-01", ExpectedCardRevision: *moved.CardRevision}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || len(snapshot.Cards) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	registry := &tools.Registry{}
	if err = registerWorkboardMutationTools(registry, bridge); err != nil {
		t.Fatal(err)
	}
	authority := &workboardMutationAuthority{t: t, allowed: true}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: authority}
	pauseArgs := map[string]any{"idempotency_key": "agent-pause-key-001", "board_id": board.BoardID,
		"card_id": card.CardID, "expected_card_revision": snapshot.Cards[0].Revision}
	paused, err := executeWorkboardMutationForTask(t, executor, "control-task", "pause-call", "workboard_request_pause", pauseArgs)
	if err != nil || paused.Effect != runtime.ConfirmedEffect {
		t.Fatalf("pause=%+v err=%v", paused, err)
	}
	replayed, err := executeWorkboardMutationForTask(t, executor, "control-task", "pause-replay", "workboard_request_pause", pauseArgs)
	if err != nil || replayed.Content != paused.Content || replayed.Effect != runtime.ConfirmedEffect {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	var pausedReceipt webui.OperationReceipt
	if json.Unmarshal([]byte(paused.Content), &pausedReceipt) != nil || pausedReceipt.Validate() != nil {
		t.Fatalf("pause receipt=%s", paused.Content)
	}
	canceled, err := executeWorkboardMutationForTask(t, executor, "control-task", "cancel-call", "workboard_request_cancel", map[string]any{
		"idempotency_key": "agent-cancel-key-01", "board_id": board.BoardID, "card_id": card.CardID,
		"expected_card_revision": *pausedReceipt.CardRevision,
	})
	if err != nil || canceled.Effect != runtime.ConfirmedEffect {
		t.Fatalf("cancel=%+v err=%v", canceled, err)
	}
	stale, err := executeWorkboardMutationForTask(t, executor, "control-task", "stale-call", "workboard_request_cancel", map[string]any{
		"idempotency_key": "agent-cancel-key-02", "board_id": board.BoardID, "card_id": card.CardID,
		"expected_card_revision": *pausedReceipt.CardRevision,
	})
	if err != nil || stale.Effect != runtime.NoEffect || !stale.Failed || !stale.Recoverable {
		t.Fatalf("stale=%+v err=%v", stale, err)
	}
	events, err := bridge.NativeEvents(ctx, board.BoardID, webui.BoardEventOptions{Limit: 100})
	if err != nil || len(events.Items) < 2 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	last := events.Items[len(events.Items)-2:]
	if last[0].Kind != webui.CardPauseRequest || last[1].Kind != webui.CardCancelRequest ||
		last[0].ActorType != "model" || last[0].ActorID == "" || last[0].ActorID != last[1].ActorID {
		t.Fatalf("control events=%+v", last)
	}
	var canceledReceipt webui.OperationReceipt
	if json.Unmarshal([]byte(canceled.Content), &canceledReceipt) != nil || canceledReceipt.Validate() != nil {
		t.Fatalf("cancel receipt=%s", canceled.Content)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	uncertain, err := executeWorkboardMutationForTask(t, executor, "control-task", "uncertain-call", "workboard_request_pause", map[string]any{
		"idempotency_key": "agent-pause-key-002", "board_id": board.BoardID, "card_id": card.CardID,
		"expected_card_revision": *canceledReceipt.CardRevision,
	})
	if err == nil || uncertain.Effect != runtime.UncertainEffect || uncertain.Recoverable {
		t.Fatalf("uncertain=%+v err=%v", uncertain, err)
	}
	wantScope := "workboard:" + board.BoardID
	if len(authority.scopes) != 5 {
		t.Fatalf("approval scopes=%v", authority.scopes)
	}
	for _, scope := range authority.scopes {
		if scope != wantScope {
			t.Fatalf("scope=%q want=%q", scope, wantScope)
		}
	}
}

func TestWorkboardControlToolSchemasAreClosedAndPolicyGated(t *testing.T) {
	for _, name := range []string{"workboard_request_pause", "workboard_request_cancel"} {
		spec := workboardControlMutationSpec(name)
		if spec.Name != name || !strings.Contains(string(spec.Parameters), `"additionalProperties":false`) {
			t.Fatalf("spec=%+v", spec)
		}
		for configured, want := range map[string]tools.Decision{"deny": tools.Deny, "ask": tools.Ask, "allow": tools.Allow} {
			if got := applicationToolPolicyFor(configured).Decide(name, "workboard:board-a"); got != want {
				t.Fatalf("%s configured=%s got=%s want=%s", name, configured, got, want)
			}
		}
		if name == "workboard_request_pause" && (!strings.Contains(spec.Description, "does not yet pause") || !strings.Contains(spec.Description, "not a pause acknowledgement")) {
			t.Fatalf("pause description overpromises runtime behavior: %q", spec.Description)
		}
	}
}
