package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestWorkboardReadToolsAreBoundedReadOnlyProjections(t *testing.T) {
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
	title := "Agent-visible board"
	receipt, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
		IdempotencyKey: "create-agent-board-0001", Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	registry := &tools.Registry{}
	if err := registerWorkboardReadTools(registry, store); err != nil {
		t.Fatal(err)
	}
	if names := []string{registry.Catalog()[0].Name, registry.Catalog()[1].Name}; !slices.Equal(names, []string{"workboard_list", "workboard_read"}) {
		t.Fatalf("catalog = %v", names)
	}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy()}
	listed, err := executor.Execute(ctx, providers.ToolCall{ID: "list-call", Name: "workboard_list", Arguments: json.RawMessage(`{"limit":1,"state":"active"}`)})
	if err != nil || listed.Failed || listed.Effect != runtime.NoEffect {
		t.Fatalf("list result=%+v err=%v", listed, err)
	}
	var page webui.Page
	if json.Unmarshal([]byte(listed.Content), &page) != nil || page.Validate() != nil || len(page.Items) != 1 || page.Items[0].ID != receipt.BoardID {
		t.Fatalf("list projection = %s", listed.Content)
	}
	read, err := executor.Execute(ctx, providers.ToolCall{ID: "read-call", Name: "workboard_read", Arguments: json.RawMessage(`{"board_id":"` + receipt.BoardID + `","limit":1}`)})
	if err != nil || read.Failed || read.Effect != runtime.NoEffect {
		t.Fatalf("read result=%+v err=%v", read, err)
	}
	var snapshot webui.BoardSnapshot
	if json.Unmarshal([]byte(read.Content), &snapshot) != nil || snapshot.Validate() != nil || snapshot.Board.ID != receipt.BoardID || snapshot.Cards == nil {
		t.Fatalf("read projection = %s", read.Content)
	}
}

func TestWorkboardReadToolsRejectNonClosedArgumentsBeforeHandler(t *testing.T) {
	ctx := context.Background()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "workboards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := &tools.Registry{}
	if err := registerWorkboardReadTools(registry, store); err != nil {
		t.Fatal(err)
	}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy()}
	invalid := []struct {
		name string
		raw  string
	}{
		{"workboard_list", `{}`},
		{"workboard_list", `{"limit":101}`},
		{"workboard_list", `{"limit":1,"secret":"x"}`},
		{"workboard_list", `{"limit":1,"limit":2}`},
		{"workboard_read", `{"board_id":"bad/id","limit":1}`},
		{"workboard_read", `{"board_id":"board","limit":1,"claim_state":"released"}`},
	}
	for index, test := range invalid {
		_, err := executor.Execute(ctx, providers.ToolCall{ID: "invalid-call", Name: test.name, Arguments: json.RawMessage(test.raw)})
		if !errors.Is(err, tools.ErrArguments) {
			t.Fatalf("invalid[%d] accepted: %v", index, err)
		}
	}
}

func TestRootAgentWorkboardAuthorityIsNotOperator(t *testing.T) {
	authority, err := (contextWorkboardAuthority{}).WorkboardAuthority(rootAgentWorkboardContext(context.Background()))
	if err != nil || authority.Validate() != nil || authority.Actor.Type != "model" || authority.Actor.ID != "darwin_root_agent" || authority.CreationScope != "root-agent-tools" {
		t.Fatalf("authority=%+v err=%v", authority, err)
	}
	if strings.Contains(authority.Actor.ID, "operator") {
		t.Fatal("root tool inherited operator identity")
	}
}

func TestWorkboardToolFailureDoesNotExposeStorageDetails(t *testing.T) {
	ctx := context.Background()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "workboards.db"))
	if err != nil {
		t.Fatal(err)
	}
	registry := &tools.Registry{}
	if err := registerWorkboardReadTools(registry, store); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := (tools.Executor{Registry: registry, Policy: applicationToolPolicy()}).Execute(ctx,
		providers.ToolCall{ID: "read-call", Name: "workboard_read", Arguments: json.RawMessage(`{"board_id":"missing","limit":1}`)})
	if err != nil || !out.Failed || !out.Recoverable || out.Effect != runtime.NoEffect || out.Content != `{"error":"workboard_unavailable"}` {
		t.Fatalf("result=%+v err=%v", out, err)
	}
}

func TestWorkboardListToolSanitizesValidOversizedProjection(t *testing.T) {
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
	description := strings.Repeat("x", webui.MaxDescriptionBytes)
	for index := range 17 {
		title := "Large board " + string(rune('A'+index))
		if _, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
			IdempotencyKey: "large-board-create-" + string(rune('A'+index)), Title: &title, Description: &description}); err != nil {
			t.Fatalf("create board %d: %v", index, err)
		}
	}
	registry := &tools.Registry{}
	if err := registerWorkboardReadTools(registry, store); err != nil {
		t.Fatal(err)
	}
	out, err := (tools.Executor{Registry: registry, Policy: applicationToolPolicy()}).Execute(ctx,
		providers.ToolCall{ID: "large-list-call", Name: "workboard_list", Arguments: json.RawMessage(`{"limit":100}`)})
	if err != nil || !out.Failed || !out.Recoverable || out.Effect != runtime.NoEffect || out.Content != `{"error":"workboard_unavailable"}` {
		t.Fatalf("oversized projection escaped as execution error: result=%+v err=%v", out, err)
	}
	failure := runtime.ToolResult{Content: `{"error":"workboard_unavailable"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}
	direct, err := workboardToolResult(map[string]string{"projection": strings.Repeat("x", maxWorkboardToolResultBytes)}, nil, failure)
	if err != nil || direct != failure {
		t.Fatalf("marshaled result cap not enforced: result=%+v err=%v", direct, err)
	}
}

type workboardCatalogProvider struct {
	t       *testing.T
	streams *atomic.Int32
	writes  bool
}

func (p workboardCatalogProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}

func (p workboardCatalogProvider) Stream(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	names := make([]string, len(request.Tools))
	for index := range request.Tools {
		names[index] = request.Tools[index].Name
	}
	if request.Model == "z" {
		if !slices.Equal(names, []string{"read_file"}) {
			p.t.Errorf("child catalog = %v, want only read_file", names)
		}
		return emit(providers.Chunk{Text: "child complete", Done: true, FinishReason: "stop"})
	}
	want := []string{"delegate", "delegate_batch", "workboard_list", "workboard_read"}
	if p.writes {
		want = []string{"delegate", "delegate_batch", "workboard_add_dependency", "workboard_archive_board", "workboard_create_board", "workboard_create_card", "workboard_list", "workboard_propose_criteria", "workboard_read", "workboard_remove_dependency", "workboard_reorder_card", "workboard_request_cancel", "workboard_request_candidate_decision", "workboard_request_pause", "workboard_request_resume", "workboard_revise_board", "workboard_transition_card", "workboard_update_card"}
	}
	if !slices.Equal(names, want) {
		p.t.Errorf("root catalog = %v, want %v", names, want)
	}
	if p.streams.Add(1) == 1 {
		if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "board-list-call", Name: "workboard_list", Arguments: json.RawMessage(`{"limit":1}`)}}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
	}
	return emit(providers.Chunk{Text: "root complete", Done: true, FinishReason: "stop"})
}

func TestConfiguredWorkboardToolsReachRootRuntimeButNotChild(t *testing.T) {
	svc, _ := autoFixture(t)
	svc.settings.Tools.WorkboardReadEnabled = true
	svc.settings.Tools.WorkboardWriteEnabled = true
	svc.settings.Workboard.Decomposition.MaxDepth = 1
	svc.settings.Workboard.Decomposition.MaxChildrenPerParent = 1
	svc.toolReviewer = func(context.Context, tools.ApprovalPrompt) (string, bool, error) { return "operator", true, nil }
	svc.settings.Workers.DelegateModel = "z"
	for index := range svc.settings.Models {
		svc.settings.Models[index].ContextTokens = 200_000
	}
	var streams atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return workboardCatalogProvider{t: t, streams: &streams, writes: true}, nil
	})
	root, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "inspect the board"})
	if err != nil || root.Text != "root complete" || streams.Load() != 2 {
		t.Fatalf("root=%+v streams=%d err=%v", root, streams.Load(), err)
	}
	svc.settings.Tools.Enabled = true
	svc.settings.Tools.ReadRoot = t.TempDir()
	svc.settings.Workers.DelegateReadTools = true
	registry, closeTools, err := readTools(svc.settings.Tools.ReadRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	store, err := telemetry.Open(context.Background(), svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := registerWorkboardReadTools(registry, store); err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridge(store, store, defaultWorkboardNow)
	if err != nil || registerWorkboardMutationTools(registry, bridge) != nil {
		t.Fatalf("register workboard mutation tools: %v", err)
	}
	if err := registerWorkboardAgentProposalTools(registry, store); err != nil {
		t.Fatalf("register workboard proposal tools: %v", err)
	}
	childContext, err := inheritDelegateTools(context.Background(), registry, applicationToolPolicy())
	if err != nil {
		t.Fatal(err)
	}
	inherited, ok := childContext.Value(delegateToolsKey{}).(*delegateTools)
	if !ok || inherited.Policy.Decide("workboard_create_card", "workboard:board") != tools.Deny {
		t.Fatal("delegated child inherited Workboard decomposition authority")
	}
	child, err := svc.runDelegate(childContext, "bounded child work", "", root.TaskID, true, "", "")
	if err != nil || child.Text != "child complete" || streams.Load() != 2 {
		t.Fatalf("child=%+v streams=%d err=%v", child, streams.Load(), err)
	}
}

func TestConfiguredWorkboardToolsRejectCloudExecutionBeforeProvider(t *testing.T) {
	svc, _ := autoFixture(t)
	svc.settings.Mode = "hybrid"
	svc.settings.Tools.WorkboardReadEnabled = true
	svc.settings.Models[0].Locality = "cloud"
	var builds atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return workboardCatalogProvider{t: t, streams: &atomic.Int32{}}, nil
	})
	if _, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "inspect"}); err == nil || builds.Load() != 0 {
		t.Fatalf("cloud workboard tool admission reached provider: builds=%d err=%v", builds.Load(), err)
	}
}

type tearingWorkboardLifecycle struct {
	delegate workboard.LifecycleSnapshotRepository
	once     sync.Once
	tear     func()
	calls    atomic.Int32
}

func (r *tearingWorkboardLifecycle) ReadCardLifecycleSnapshots(ctx context.Context, boardID string, cardIDs []string) (map[string]workboard.CardLifecycleSnapshot, error) {
	r.calls.Add(1)
	r.once.Do(r.tear)
	return r.delegate.ReadCardLifecycleSnapshots(ctx, boardID, cardIDs)
}

func TestRootAgentReadRetriesTornCardAndLifecycleComposition(t *testing.T) {
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
	if _, err := bridge.RootAgentRead(nil, "board", webui.BoardSnapshotOptions{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("nil context was not rejected: %v", err)
	}
	boardTitle := "Coherence board"
	board, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
		IdempotencyKey: "coherence-board-key-01", Title: &boardTitle})
	if err != nil {
		t.Fatal(err)
	}
	cardTitle, before := "Coherence card", "before lifecycle read"
	criteria := []webui.AcceptanceCriterion{{Version: 1, ID: "coherence-check", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "coherence-validator", Description: "Coherent card and lifecycle", Required: true}}
	card, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCreate,
		IdempotencyKey: "coherence-card-key-001", BoardID: board.BoardID, Title: &cardTitle, Description: &before,
		Criteria: criteria, ExpectedBoardRevision: revisionPointer(board.BoardRevision), ExpectedGraphRevision: revisionPointer(1)})
	if err != nil || card.CardRevision == nil {
		t.Fatalf("create card=%+v err=%v", card, err)
	}
	after := "after lifecycle read"
	tearing := &tearingWorkboardLifecycle{delegate: store}
	tearing.tear = func() {
		if _, mutationErr := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardRevise,
			IdempotencyKey: "coherence-revise-key-01", BoardID: board.BoardID, CardID: card.CardID,
			Description: &after, ExpectedCardRevision: card.CardRevision}); mutationErr != nil {
			t.Errorf("tear mutation: %v", mutationErr)
		}
	}
	bridge.lifecycle = tearing
	snapshot, err := bridge.RootAgentRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || snapshot.Validate() != nil || len(snapshot.Cards) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.Cards[0].Description != after || snapshot.Cards[0].Revision != *card.CardRevision+1 || tearing.calls.Load() != 2 {
		t.Fatalf("torn snapshot escaped or retry missing: card=%+v lifecycle_calls=%d", snapshot.Cards[0], tearing.calls.Load())
	}
}
