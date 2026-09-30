package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

type workboardMutationAuthority struct {
	t       *testing.T
	scopes  []string
	allowed bool
}

func (a *workboardMutationAuthority) ExecuteApproved(ctx context.Context, authorization tools.Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
	a.scopes = append(a.scopes, authorization.Scope)
	if authorization.ToolBehavior != runtime.BehaviorIdempotentWrite {
		a.t.Fatalf("behavior = %q", authorization.ToolBehavior)
	}
	if !a.allowed {
		return runtime.ToolResult{Effect: runtime.NoEffect}, tools.ErrDenied
	}
	return invoke(ctx)
}

func executeWorkboardMutation(t *testing.T, executor tools.Executor, callID, name string, arguments any) (runtime.ToolResult, error) {
	return executeWorkboardMutationForTask(t, executor, "task", callID, name, arguments)
}

func executeWorkboardMutationForTask(t *testing.T, executor tools.Executor, taskID, callID, name string, arguments any) (runtime.ToolResult, error) {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return executor.ExecuteScoped(context.Background(), runtime.ToolExecution{TaskID: taskID, SessionID: "session", TurnID: "turn", AttemptID: "attempt",
		Call: providers.ToolCall{ID: callID, Name: name, Arguments: raw}})
}

// workboardMutationJournal exercises a write handler at the same durable
// boundary used by the runtime: the model turn and tool start are committed
// before the approved handler receives its scoped execution identity.
type workboardMutationJournal struct {
	t                     *testing.T
	store                 *telemetry.Store
	taskID, sessionID     string
	sequence, turnOrdinal int64
}

func newWorkboardMutationJournal(t *testing.T, store *telemetry.Store) *workboardMutationJournal {
	t.Helper()
	j := &workboardMutationJournal{t: t, store: store, taskID: "task", sessionID: "session"}
	j.append(runtime.Event{Kind: runtime.TaskStarted})
	return j
}

func (j *workboardMutationJournal) append(event runtime.Event) {
	j.t.Helper()
	j.sequence++
	event.Version, event.ID = 1, fmt.Sprintf("workboard-agent-event-%d", j.sequence)
	event.TaskID, event.SessionID, event.CorrelationID = j.taskID, j.sessionID, j.taskID
	event.Sequence, event.Time = j.sequence, time.Unix(1_800_000_000+j.sequence, 0).UTC()
	if err := j.store.Append(context.Background(), j.sequence-1, event); err != nil {
		j.t.Fatalf("append %s: %v", event.Kind, err)
	}
}

func (j *workboardMutationJournal) execute(executor tools.Executor, callID, name string, arguments any) (runtime.ToolResult, error) {
	j.t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		j.t.Fatal(err)
	}
	j.turnOrdinal++
	turnID := fmt.Sprintf("workboard-agent-turn-%d", j.turnOrdinal)
	attemptID := fmt.Sprintf("workboard-agent-attempt-%d", j.turnOrdinal)
	call := providers.ToolCall{ID: callID, Name: name, Arguments: raw}
	j.append(runtime.Event{Kind: runtime.TurnStarted, TurnID: turnID, AttemptID: attemptID,
		Data: runtime.Data{ModelID: "coordinator-model", ProviderID: "coordinator-provider"}})
	j.append(runtime.Event{Kind: runtime.TurnCompleted, TurnID: turnID, AttemptID: attemptID,
		Data: runtime.Data{FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{call}}})
	j.append(runtime.Event{Kind: runtime.ToolStarted, TurnID: turnID, AttemptID: attemptID,
		Data: runtime.Data{ToolCallID: callID, ToolName: name, ToolBehavior: runtime.BehaviorIdempotentWrite, Effect: runtime.UncertainEffect}})
	out, executeErr := executor.ExecuteScoped(context.Background(), runtime.ToolExecution{TaskID: j.taskID, SessionID: j.sessionID,
		TurnID: turnID, AttemptID: attemptID, Call: call})
	code := ""
	if executeErr != nil || out.Failed {
		code = "tool_failed"
	}
	j.append(runtime.Event{Kind: runtime.ToolCompleted, TurnID: turnID, AttemptID: attemptID,
		Data: runtime.Data{ToolCallID: callID, ToolName: name, ToolBehavior: runtime.BehaviorIdempotentWrite, Effect: out.Effect, Text: out.Content, Code: code}})
	return out, executeErr
}

func TestRootAgentWorkboardMutationToolsCreateRichCardAndReplay(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "workboards.db")
	store, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	settings := config.Defaults()
	configID, err := settingsConfigID(settings)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := configuredWorkboardDecompositionPolicy(settings, configID)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridgeWithDecomposition(store, store, defaultWorkboardNow, policy)
	if err != nil {
		t.Fatal(err)
	}
	registry := &tools.Registry{}
	if err = registerWorkboardMutationTools(registry, bridge); err != nil {
		t.Fatal(err)
	}
	authority := &workboardMutationAuthority{t: t, allowed: true}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: authority}

	boardOut, err := executeWorkboardMutation(t, executor, "board-call", "workboard_create_board", map[string]any{
		"idempotency_key": "agent-board-key-0001", "title": "Agent board", "description": "Local root agent work",
	})
	if err != nil || boardOut.Effect != runtime.ConfirmedEffect {
		t.Fatalf("create board: out=%+v err=%v", boardOut, err)
	}
	var board webui.OperationReceipt
	if json.Unmarshal([]byte(boardOut.Content), &board) != nil || board.Validate() != nil {
		t.Fatalf("board receipt = %s", boardOut.Content)
	}
	journal := newWorkboardMutationJournal(t, store)
	criterion := map[string]any{"version": 1, "id": "go-tests", "kind": "objective", "required_source": "deterministic", "validator_id": "go-test", "description": "Focused tests pass", "required": true}
	baseArgs := map[string]any{"idempotency_key": "agent-card-key-00001", "board_id": board.BoardID, "title": "Base card", "criteria": []any{criterion},
		"expected_board_revision": board.BoardRevision, "expected_graph_revision": 1}
	baseOut, err := journal.execute(executor, "base-call", "workboard_create_card", baseArgs)
	if err != nil || baseOut.Effect != runtime.ConfirmedEffect {
		t.Fatalf("create base card: out=%+v err=%v", baseOut, err)
	}
	var base webui.OperationReceipt
	if json.Unmarshal([]byte(baseOut.Content), &base) != nil || base.CardID == "" {
		t.Fatalf("base receipt = %s", baseOut.Content)
	}
	snapshot, err := bridge.RootAgentRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	richArgs := map[string]any{"idempotency_key": "agent-card-key-00002", "board_id": board.BoardID, "title": "Rich card", "description": "All bounded decomposition fields",
		"priority": "high", "parent_id": base.CardID, "assignee_id": "worker_one", "labels": []string{"runtime", "testing"}, "dependencies": []string{base.CardID},
		"budget": map[string]any{"attempt_limit": 3, "time_limit_ms": 60000, "token_limit": 100000, "cost_micros": 50000}, "criteria": []any{criterion},
		"expected_board_revision": snapshot.Board.Revision, "expected_graph_revision": snapshot.GraphRevision}
	richOut, err := journal.execute(executor, "rich-call", "workboard_create_card", richArgs)
	if err != nil || richOut.Effect != runtime.ConfirmedEffect {
		t.Fatalf("create rich card: out=%+v err=%v", richOut, err)
	}
	var richReceipt webui.OperationReceipt
	if json.Unmarshal([]byte(richOut.Content), &richReceipt) != nil || richReceipt.Validate() != nil {
		t.Fatalf("rich receipt = %s", richOut.Content)
	}
	replayOut, err := journal.execute(executor, "replay-call", "workboard_create_card", richArgs)
	if err != nil || replayOut.Effect != runtime.ConfirmedEffect || replayOut.Content != richOut.Content {
		t.Fatalf("replay: out=%+v err=%v", replayOut, err)
	}
	raw, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var admissionBody []byte
	if err = raw.QueryRow(`SELECT body FROM workboard_decomposition_admissions WHERE operation_id=?`, richReceipt.OperationID).Scan(&admissionBody); err != nil {
		t.Fatal(err)
	}
	var admission workboard.DecompositionAdmission
	if json.Unmarshal(admissionBody, &admission) != nil || admission.Validate() != nil || admission.ConfigDigest != configID ||
		admission.Origin.TaskID != journal.taskID || admission.Origin.SessionID != journal.sessionID ||
		admission.Origin.TurnID != "workboard-agent-turn-2" || admission.Origin.AttemptID != "workboard-agent-attempt-2" ||
		admission.Origin.ToolCallID != "rich-call" || admission.Origin.ToolName != "workboard_create_card" ||
		admission.Origin.ModelID != "coordinator-model" || admission.Origin.ProviderID != "coordinator-provider" {
		t.Fatalf("durable runtime origin = %+v", admission)
	}
	snapshot, err = bridge.RootAgentRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || len(snapshot.Cards) != 2 {
		t.Fatalf("snapshot cards=%d err=%v", len(snapshot.Cards), err)
	}
	var rich webui.Card
	for _, card := range snapshot.Cards {
		if card.Title == "Rich card" {
			rich = card
		}
	}
	if rich.ID == "" || rich.ParentID != base.CardID || rich.AssigneeID != "worker_one" || len(rich.Labels) != 2 || len(rich.Dependencies) != 1 || rich.Budget.AttemptLimit != 3 {
		t.Fatalf("rich card = %+v", rich)
	}
	updateOut, err := journal.execute(executor, "update-call", "workboard_update_card", map[string]any{
		"idempotency_key": "agent-update-key-001", "board_id": board.BoardID, "card_id": rich.ID, "expected_card_revision": rich.Revision,
		"expected_graph_revision": snapshot.GraphRevision, "clear_parent": true, "clear_assignee": true, "labels": []string{},
		"budget": map[string]any{"attempt_limit": 2, "time_limit_ms": 0, "token_limit": 0, "cost_micros": 0},
	})
	if err != nil || updateOut.Effect != runtime.ConfirmedEffect {
		t.Fatalf("update rich card: out=%+v err=%v", updateOut, err)
	}
	snapshot, err = bridge.RootAgentRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range snapshot.Cards {
		if card.ID == rich.ID {
			rich = card
		}
	}
	if rich.ParentID != "" || rich.AssigneeID != "" || rich.Labels == nil || len(rich.Labels) != 0 || rich.Budget.AttemptLimit != 2 {
		t.Fatalf("updated rich card = %+v", rich)
	}
	changeDependency := func(callID, toolName, key string) {
		t.Helper()
		out, mutationErr := executeWorkboardMutation(t, executor, callID, toolName, map[string]any{
			"idempotency_key": key, "board_id": board.BoardID, "card_id": rich.ID, "dependency_id": base.CardID,
			"expected_card_revision": rich.Revision, "expected_graph_revision": snapshot.GraphRevision,
		})
		if mutationErr != nil || out.Effect != runtime.ConfirmedEffect {
			t.Fatalf("%s: out=%+v err=%v", toolName, out, mutationErr)
		}
		snapshot, mutationErr = bridge.RootAgentRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
		if mutationErr != nil {
			t.Fatal(mutationErr)
		}
		for _, card := range snapshot.Cards {
			if card.ID == rich.ID {
				rich = card
			}
		}
	}
	changeDependency("remove-one", "workboard_remove_dependency", "agent-remove-key-001")
	changeDependency("add-one", "workboard_add_dependency", "agent-add-key-000001")
	changeDependency("remove-two", "workboard_remove_dependency", "agent-remove-key-002")
	reorderArgs := map[string]any{
		"idempotency_key": "agent-reorder-key-01", "board_id": board.BoardID, "card_id": rich.ID, "before_card_id": base.CardID,
		"expected_board_revision": snapshot.Board.Revision, "expected_layout_revision": snapshot.Board.LayoutRevision, "expected_card_revision": rich.Revision,
	}
	reordered, err := executeWorkboardMutation(t, executor, "reorder-call", "workboard_reorder_card", reorderArgs)
	if err != nil || reordered.Effect != runtime.ConfirmedEffect {
		t.Fatalf("reorder card: out=%+v err=%v", reordered, err)
	}
	reorderReplay, err := executeWorkboardMutation(t, executor, "reorder-replay", "workboard_reorder_card", reorderArgs)
	if err != nil || reorderReplay.Effect != runtime.ConfirmedEffect || reorderReplay.Content != reordered.Content {
		t.Fatalf("reorder replay: out=%+v err=%v", reorderReplay, err)
	}
	snapshot, err = bridge.RootAgentRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || len(snapshot.Cards) != 2 || snapshot.Cards[0].ID != rich.ID || snapshot.Cards[1].ID != base.CardID {
		t.Fatalf("reordered snapshot=%+v err=%v", snapshot.Cards, err)
	}
	rich = snapshot.Cards[0]
	moveOut, err := executeWorkboardMutation(t, executor, "move-call", "workboard_transition_card", map[string]any{
		"idempotency_key": "agent-move-key-00001", "board_id": board.BoardID, "card_id": rich.ID, "target_state": "ready",
		"expected_board_revision": snapshot.Board.Revision, "expected_layout_revision": snapshot.Board.LayoutRevision, "expected_card_revision": rich.Revision,
	})
	if err != nil || moveOut.Effect != runtime.ConfirmedEffect {
		t.Fatalf("transition card: out=%+v err=%v", moveOut, err)
	}
	if len(authority.scopes) != 11 || authority.scopes[0] != "workboards" || authority.scopes[1] != "workboard:"+board.BoardID || authority.scopes[10] != "workboard:"+board.BoardID {
		t.Fatalf("approval scopes = %v", authority.scopes)
	}
	events, err := bridge.NativeEvents(ctx, board.BoardID, webui.BoardEventOptions{Limit: 100})
	if err != nil || len(events.Items) != 9 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	var actorID string
	for _, event := range events.Items {
		if event.ActorType != "model" || event.ActorID == "" || event.ActorID == "darwin_root_agent" {
			t.Fatalf("untrusted actor escaped: %+v", event)
		}
		if actorID == "" {
			actorID = event.ActorID
		} else if event.ActorID != actorID {
			t.Fatalf("one task used multiple model actors: first=%q event=%+v", actorID, event)
		}
	}
}

func TestRootAgentWorkboardMutationToolsReviseAndArchiveBoard(t *testing.T) {
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
	registry := &tools.Registry{}
	if err = registerWorkboardMutationTools(registry, bridge); err != nil {
		t.Fatal(err)
	}
	authority := &workboardMutationAuthority{t: t, allowed: true}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: authority}

	created, err := executeWorkboardMutation(t, executor, "create-board", "workboard_create_board", map[string]any{
		"idempotency_key": "board-lifecycle-create-01", "title": "Initial", "description": "Initial description",
	})
	if err != nil || created.Effect != runtime.ConfirmedEffect {
		t.Fatalf("create: out=%+v err=%v", created, err)
	}
	var createReceipt webui.OperationReceipt
	if json.Unmarshal([]byte(created.Content), &createReceipt) != nil || createReceipt.Validate() != nil {
		t.Fatalf("create receipt = %s", created.Content)
	}
	reviseArgs := map[string]any{"idempotency_key": "board-lifecycle-revise-01", "board_id": createReceipt.BoardID,
		"title": "Revised", "description": "", "expected_board_revision": createReceipt.BoardRevision}
	revised, err := executeWorkboardMutation(t, executor, "revise-board", "workboard_revise_board", reviseArgs)
	if err != nil || revised.Effect != runtime.ConfirmedEffect {
		t.Fatalf("revise: out=%+v err=%v", revised, err)
	}
	replayed, err := executeWorkboardMutation(t, executor, "replay-revise", "workboard_revise_board", reviseArgs)
	if err != nil || replayed.Effect != runtime.ConfirmedEffect || replayed.Content != revised.Content {
		t.Fatalf("revise replay: out=%+v err=%v", replayed, err)
	}
	var reviseReceipt webui.OperationReceipt
	if json.Unmarshal([]byte(revised.Content), &reviseReceipt) != nil || reviseReceipt.BoardRevision != createReceipt.BoardRevision+1 {
		t.Fatalf("revise receipt = %s", revised.Content)
	}
	snapshot, err := bridge.RootAgentRead(ctx, createReceipt.BoardID, webui.BoardSnapshotOptions{Limit: 10})
	if err != nil || snapshot.Board.Title != "Revised" || snapshot.Board.Description != "" {
		t.Fatalf("revised board=%+v err=%v", snapshot.Board, err)
	}
	stale, err := executeWorkboardMutation(t, executor, "stale-archive", "workboard_archive_board", map[string]any{
		"idempotency_key": "board-lifecycle-stale-01", "board_id": createReceipt.BoardID, "expected_board_revision": createReceipt.BoardRevision,
	})
	if err != nil || !stale.Failed || !stale.Recoverable || stale.Effect != runtime.NoEffect {
		t.Fatalf("stale archive: out=%+v err=%v", stale, err)
	}
	archived, err := executeWorkboardMutation(t, executor, "archive-board", "workboard_archive_board", map[string]any{
		"idempotency_key": "board-lifecycle-archive-01", "board_id": createReceipt.BoardID, "expected_board_revision": reviseReceipt.BoardRevision,
	})
	if err != nil || archived.Effect != runtime.ConfirmedEffect {
		t.Fatalf("archive: out=%+v err=%v", archived, err)
	}
	archiveReplay, err := executeWorkboardMutation(t, executor, "replay-archive", "workboard_archive_board", map[string]any{
		"idempotency_key": "board-lifecycle-archive-01", "board_id": createReceipt.BoardID, "expected_board_revision": reviseReceipt.BoardRevision,
	})
	if err != nil || archiveReplay.Effect != runtime.ConfirmedEffect || archiveReplay.Content != archived.Content {
		t.Fatalf("archive replay: out=%+v err=%v", archiveReplay, err)
	}
	page, err := bridge.RootAgentList(ctx, webui.BoardListOptions{Limit: 10, State: "archived"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != createReceipt.BoardID || page.Items[0].State != "archived" {
		t.Fatalf("archived page=%+v err=%v", page, err)
	}
	wantScope := "workboard:" + createReceipt.BoardID
	if len(authority.scopes) != 6 || authority.scopes[0] != "workboards" {
		t.Fatalf("approval scopes = %v", authority.scopes)
	}
	for _, scope := range authority.scopes[1:] {
		if scope != wantScope {
			t.Fatalf("approval scopes = %v", authority.scopes)
		}
	}
	events, err := bridge.NativeEvents(ctx, createReceipt.BoardID, webui.BoardEventOptions{Limit: 10})
	if err != nil || len(events.Items) != 3 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	for _, event := range events.Items {
		if event.ActorType != "model" || event.ActorID == "" || event.ActorID == "darwin_root_agent" {
			t.Fatalf("event attribution = %+v", event)
		}
	}
}

func TestRootAgentWorkboardMutationAttributionDistinguishesTasks(t *testing.T) {
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
	registry := &tools.Registry{}
	if err = registerWorkboardMutationTools(registry, bridge); err != nil {
		t.Fatal(err)
	}
	untrustedTitle := "Untrusted board"
	if _, untrustedErr := bridge.RootAgentMutate(ctx, webui.BoardRequest{Version: webui.ContractVersion, Action: webui.BoardCreate,
		IdempotencyKey: "untrusted-direct-key-01", Title: &untrustedTitle}); untrustedErr == nil {
		t.Fatal("root mutation accepted without scoped execution identity")
	}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: &workboardMutationAuthority{t: t, allowed: true}}
	actors := map[string]bool{}
	for index, taskID := range []string{"task-one", "task-two"} {
		out, executeErr := executeWorkboardMutationForTask(t, executor, taskID, "call-"+taskID, "workboard_create_board", map[string]any{
			"idempotency_key": "attribution-key-000" + string(rune('1'+index)), "title": "Board " + taskID,
		})
		if executeErr != nil || out.Effect != runtime.ConfirmedEffect {
			t.Fatalf("task %q create: out=%+v err=%v", taskID, out, executeErr)
		}
		var receipt webui.OperationReceipt
		if json.Unmarshal([]byte(out.Content), &receipt) != nil {
			t.Fatal("invalid receipt")
		}
		events, readErr := bridge.NativeEvents(ctx, receipt.BoardID, webui.BoardEventOptions{Limit: 10})
		if readErr != nil || len(events.Items) != 1 || events.Items[0].ActorType != "model" {
			t.Fatalf("events=%+v err=%v", events, readErr)
		}
		actors[events.Items[0].ActorID] = true
	}
	if len(actors) != 2 {
		t.Fatalf("separate tasks shared attribution: %v", actors)
	}
}

func TestRootAgentWorkboardMutationToolsClassifyDefinitiveAndUncertainFailure(t *testing.T) {
	ctx := context.Background()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "workboards.db"))
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridge(store, store, defaultWorkboardNow)
	if err != nil {
		t.Fatal(err)
	}
	registry := &tools.Registry{}
	if err = registerWorkboardMutationTools(registry, bridge); err != nil {
		t.Fatal(err)
	}
	authority := &workboardMutationAuthority{t: t, allowed: true}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: authority}
	boardOut, err := executeWorkboardMutation(t, executor, "board", "workboard_create_board", map[string]any{"idempotency_key": "failure-board-key-01", "title": "Failure board"})
	if err != nil {
		t.Fatal(err)
	}
	var board webui.OperationReceipt
	if json.Unmarshal([]byte(boardOut.Content), &board) != nil {
		t.Fatal("invalid board receipt")
	}
	criterion := map[string]any{"version": 1, "id": "criterion", "kind": "objective", "required_source": "deterministic", "validator_id": "validator", "description": "Pass", "required": true}
	out, err := executeWorkboardMutation(t, executor, "stale", "workboard_create_card", map[string]any{"idempotency_key": "failure-card-key-01", "board_id": board.BoardID,
		"title": "Stale", "criteria": []any{criterion}, "expected_board_revision": 999, "expected_graph_revision": 1})
	if err != nil || !out.Failed || !out.Recoverable || out.Effect != runtime.NoEffect || out.Content != `{"error":"workboard_mutation_rejected","code":"stale_revision"}` {
		t.Fatalf("definitive failure: out=%+v err=%v", out, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	out, err = executeWorkboardMutation(t, executor, "uncertain", "workboard_create_card", map[string]any{"idempotency_key": "failure-card-key-02", "board_id": board.BoardID,
		"title": "Uncertain", "criteria": []any{criterion}, "expected_board_revision": board.BoardRevision, "expected_graph_revision": 1})
	if !errors.Is(err, tools.ErrExecution) || out.Effect != runtime.UncertainEffect || out.Content != "" || out.Recoverable {
		t.Fatalf("uncertain failure: out=%+v err=%v", out, err)
	}
}

func TestWorkboardMutationNoEffectWhitelistExcludesInvalidReplayReceipt(t *testing.T) {
	for _, test := range []struct {
		code       workboard.ErrorCode
		definitive bool
	}{
		{workboard.CodeStaleRevision, true},
		{workboard.CodeIllegalTransition, true},
		{workboard.CodeCycle, true},
		{workboard.CodeInvalid, false},
		{workboard.CodeLeaseOwner, false},
	} {
		err := &workboard.Violation{Code: test.code, Field: "stored_receipt"}
		if got := definitiveWorkboardNoEffect(err); (got != nil) != test.definitive {
			t.Fatalf("code %q definitive=%v", test.code, got != nil)
		}
	}
	if definitiveWorkboardNoEffect(errors.New("database acknowledgement lost")) != nil {
		t.Fatal("generic storage error classified as no effect")
	}
}

func TestRootAgentWorkboardMutationSchemasAreClosedAndBounded(t *testing.T) {
	registry := &tools.Registry{}
	store, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "workboards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bridge, err := NewWorkboardBridge(store, store, defaultWorkboardNow)
	if err != nil || registerWorkboardMutationTools(registry, bridge) != nil {
		t.Fatalf("register: %v", err)
	}
	authority := &workboardMutationAuthority{t: t, allowed: true}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: authority}
	criterion := map[string]any{"version": 1, "id": "criterion", "kind": "objective", "required_source": "deterministic",
		"validator_id": "validator", "description": "Pass", "required": true}
	for index, forged := range []map[string]any{
		{"config_digest": strings.Repeat("a", 64)},
		{"policy_digest": strings.Repeat("b", 64)},
		{"decomposition": map[string]any{"version": 1, "max_depth": 64, "max_children": 64}},
		{"admission_id": "forged-admission"},
	} {
		arguments := map[string]any{"idempotency_key": "forged-policy-key-0" + string(rune('1'+index)), "board_id": "board", "title": "Card",
			"criteria": []any{criterion}, "expected_board_revision": 1, "expected_graph_revision": 1}
		for key, value := range forged {
			arguments[key] = value
		}
		if _, err := executeWorkboardMutation(t, executor, "forged-policy-call", "workboard_create_card", arguments); !errors.Is(err, tools.ErrArguments) {
			t.Fatalf("forged policy case %d accepted: %v", index, err)
		}
	}
	for index, arguments := range []map[string]any{
		{"idempotency_key": "bad-extra-key-0001", "title": "Board", "actor_id": "operator"},
		{"idempotency_key": "bad-revise-key-0001", "board_id": "board", "expected_board_revision": 1},
		{"idempotency_key": "bad-archive-key-001", "board_id": "board", "expected_board_revision": 1, "reason": "done"},
		{"idempotency_key": "bad-labels-key-001", "board_id": "board", "card_id": "card", "expected_card_revision": 1, "labels": make([]string, 33)},
		{"idempotency_key": "bad-parent-key-001", "board_id": "board", "card_id": "card", "expected_card_revision": 1, "parent_id": "parent"},
	} {
		name := []string{"workboard_create_board", "workboard_revise_board", "workboard_archive_board", "workboard_update_card", "workboard_update_card"}[index]
		if _, err := executeWorkboardMutation(t, executor, "bad-call", name, arguments); !errors.Is(err, tools.ErrArguments) {
			t.Fatalf("case %d accepted: %v", index, err)
		}
	}
	if len(authority.scopes) != 0 {
		t.Fatalf("invalid arguments reached approval: %v", authority.scopes)
	}
}

func TestRootAgentWorkboardMutationSemanticValidationIsRecoverableAndEffectFree(t *testing.T) {
	store, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "workboards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bridge, err := NewWorkboardBridge(store, store, defaultWorkboardNow)
	if err != nil {
		t.Fatal(err)
	}
	registry := &tools.Registry{}
	if err = registerWorkboardMutationTools(registry, bridge); err != nil {
		t.Fatal(err)
	}
	authority := &workboardMutationAuthority{t: t, allowed: true}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: authority}
	for index, arguments := range []map[string]any{
		{"idempotency_key": "semantic-title-key-01", "title": strings.Repeat("🙂", 200)},
		{"idempotency_key": "semantic-revise-key-1", "board_id": "missing_board", "title": strings.Repeat("🙂", 200), "expected_board_revision": 1},
		{"idempotency_key": "semantic-label-key-01", "board_id": "missing_board", "card_id": "card", "expected_card_revision": 1, "labels": []string{"Runtime", " runtime "}},
	} {
		name := []string{"workboard_create_board", "workboard_revise_board", "workboard_update_card"}[index]
		out, executeErr := executeWorkboardMutation(t, executor, "semantic-call-"+string(rune('1'+index)), name, arguments)
		if executeErr != nil || !out.Failed || !out.Recoverable || out.Effect != runtime.NoEffect || out.Content != `{"error":"workboard_mutation_invalid"}` {
			t.Fatalf("case %d: out=%+v err=%v", index, out, executeErr)
		}
	}
	if len(authority.scopes) != 3 || authority.scopes[0] != "workboards" || authority.scopes[1] != "workboard:missing_board" || authority.scopes[2] != "workboard:missing_board" {
		t.Fatalf("semantic validation did not remain behind approval: %v", authority.scopes)
	}
	page, err := bridge.RootAgentList(context.Background(), webui.BoardListOptions{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("semantic rejection mutated boards: page=%+v err=%v", page, err)
	}
}
