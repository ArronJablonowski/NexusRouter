package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type bridgeBoardRepository struct {
	page              workboard.BoardPage
	events            workboard.BoardEventPage
	createReceipt     workboard.OperationReceipt
	createScope       string
	createActor       workboard.Actor
	createCalls       int
	archiveErr        error
	eventOptions      workboard.BoardEventOptions
	control           workboard.ControlMutation
	evaluation        workboard.EvaluationMutation
	dependencyPage    workboard.DependencyPage
	dependencyOptions workboard.DependencyOptions
	graph             workboard.Graph
	cardMutation      workboard.CardMutation
}

func (r *bridgeBoardRepository) CreateWorkboard(_ context.Context, scope string, _ workboard.CreateBoardRequest, actor workboard.Actor, _ time.Time) (workboard.OperationReceipt, error) {
	r.createCalls++
	r.createScope, r.createActor = scope, actor
	return r.createReceipt, nil
}
func (r *bridgeBoardRepository) ReviseWorkboard(context.Context, workboard.ReviseBoardRequest, workboard.Actor, time.Time) (workboard.OperationReceipt, error) {
	return workboard.OperationReceipt{}, nil
}
func (r *bridgeBoardRepository) ArchiveWorkboard(context.Context, workboard.ArchiveBoardRequest, workboard.Actor, time.Time) (workboard.OperationReceipt, error) {
	return workboard.OperationReceipt{}, r.archiveErr
}
func (r *bridgeBoardRepository) ListWorkboards(context.Context, workboard.BoardListOptions) (workboard.BoardPage, error) {
	return r.page, nil
}
func (r *bridgeBoardRepository) ReadWorkboard(context.Context, string, workboard.BoardSnapshotOptions) (workboard.BoardSnapshot, error) {
	return workboard.BoardSnapshot{}, nil
}
func (r *bridgeBoardRepository) ListWorkboardEvents(_ context.Context, _ string, options workboard.BoardEventOptions) (workboard.BoardEventPage, error) {
	r.eventOptions = options
	return r.events, nil
}
func (r *bridgeBoardRepository) GetCard(context.Context, string, string) (workboard.Card, error) {
	return workboard.Card{}, nil
}
func (r *bridgeBoardRepository) ListCards(context.Context, string, workboard.CardFilter) (workboard.CardPage, error) {
	return workboard.CardPage{}, nil
}
func (r *bridgeBoardRepository) LoadGraph(context.Context, string) (workboard.Graph, error) {
	return r.graph, nil
}
func (r *bridgeBoardRepository) ListDependencyEdges(_ context.Context, _, _ string, options workboard.DependencyOptions) (workboard.DependencyPage, error) {
	r.dependencyOptions = options
	return r.dependencyPage, nil
}
func (r *bridgeBoardRepository) ApplyCardMutation(_ context.Context, mutation workboard.CardMutation) (workboard.CardMutationResult, error) {
	r.cardMutation = mutation
	return workboard.CardMutationResult{}, nil
}
func (r *bridgeBoardRepository) ApplyProgressMutation(context.Context, workboard.ProgressMutation) (workboard.OperationReceipt, error) {
	return workboard.OperationReceipt{}, nil
}
func (r *bridgeBoardRepository) ReplayProgressMutation(context.Context, workboard.ProgressMutation) (workboard.OperationReceipt, bool, error) {
	return workboard.OperationReceipt{}, false, nil
}
func (r *bridgeBoardRepository) ReadCardLifecycleSnapshots(context.Context, string, []string) (map[string]workboard.CardLifecycleSnapshot, error) {
	return map[string]workboard.CardLifecycleSnapshot{}, nil
}

func (r *bridgeBoardRepository) ListAttemptHistory(context.Context, string, string, workboard.AttemptHistoryOptions) (workboard.AttemptHistoryPage, error) {
	return workboard.AttemptHistoryPage{}, errors.New("not implemented")
}

func (r *bridgeBoardRepository) ReadAttemptDetail(context.Context, string, string, string, workboard.AttemptDetailOptions) (workboard.AttemptDetailPage, error) {
	return workboard.AttemptDetailPage{}, errors.New("not implemented")
}
func (r *bridgeBoardRepository) ApplyControlMutation(_ context.Context, mutation workboard.ControlMutation) (workboard.OperationReceipt, error) {
	r.control = mutation
	return workboard.OperationReceipt{}, errors.New("control sentinel")
}
func (r *bridgeBoardRepository) ReplayControlMutation(context.Context, workboard.ControlMutation) (workboard.OperationReceipt, bool, error) {
	return workboard.OperationReceipt{}, false, nil
}
func (r *bridgeBoardRepository) ApplyEvaluationMutation(_ context.Context, mutation workboard.EvaluationMutation, _ func() time.Time) (workboard.OperationReceipt, error) {
	r.evaluation = mutation
	return workboard.OperationReceipt{}, errors.New("evaluation sentinel")
}
func (r *bridgeBoardRepository) ReplayEvaluationMutation(context.Context, workboard.EvaluationMutation) (workboard.OperationReceipt, bool, error) {
	return workboard.OperationReceipt{}, false, nil
}
func (r *bridgeBoardRepository) PrepareCandidateEvaluation(context.Context, workboard.EvaluationMutation) (workboard.CandidateEvaluationRequest, error) {
	return workboard.CandidateEvaluationRequest{}, errors.New("evaluation preparation sentinel")
}

func TestWorkboardBridgeMapsTrustedListAndEvents(t *testing.T) {
	now := time.Date(2026, 9, 9, 19, 0, 0, 0, time.UTC)
	board := workboard.Board{Version: 1, ID: "board-a", Revision: 1, LayoutRevision: 1, EventSequence: 1, State: "active", Title: "Board", CardCount: 0, CreatedAt: now, UpdatedAt: now}
	repository := &bridgeBoardRepository{
		page: workboard.BoardPage{Version: 1, Items: []workboard.Board{board}},
		events: workboard.BoardEventPage{Version: 1, BoardID: board.ID, HighWaterSequence: 1, Items: []workboard.BoardEvent{{Version: 1, ID: "event-a", BoardID: board.ID,
			Sequence: 1, OperationID: "operation-key-01", Kind: workboard.BoardCreateAction, ActorID: "api_operator", ActorType: "operator", CreatedAt: now}}},
	}
	bridge, err := NewWorkboardBridge(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	page, err := bridge.NativeList(context.Background(), webui.BoardListOptions{Limit: 25})
	if err != nil || page.Validate() != nil || len(page.Items) != 1 || page.Items[0].ID != board.ID {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	events, err := bridge.BrowserEvents(context.Background(), strings.Repeat("a", 64), board.ID, webui.BoardEventOptions{Limit: 100, TailAfterSequence: 1})
	if err != nil || events.Validate() != nil || len(events.Items) != 1 || events.Items[0].Kind != webui.BoardCreate || repository.eventOptions.TailAfterSequence != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestWorkboardBridgeRejectsUntrustedBrowserSubject(t *testing.T) {
	repository := &bridgeBoardRepository{}
	bridge, err := NewWorkboardBridge(repository, repository, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.BrowserList(context.Background(), "bad subject", webui.BoardListOptions{Limit: 25}); err == nil {
		t.Fatal("invalid browser subject accepted")
	}
}

func TestWorkboardBridgeBindsBrowserMutationAuthority(t *testing.T) {
	now := time.Date(2026, 9, 9, 19, 0, 0, 0, time.UTC)
	repository := &bridgeBoardRepository{createReceipt: workboard.OperationReceipt{Version: 1, BoardID: "board-a", OperationID: "operation-key-01",
		RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1,
		EventCount: 1, TransactionBytes: 128, BoardRevision: 1, Outcome: "committed", CreatedAt: now}}
	authority, err := BrowserWorkboardAuthority("stable-browser-authority-fixture")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridgeWithBrowserAuthority(repository, repository, func() time.Time { return now }, authority)
	if err != nil {
		t.Fatal(err)
	}
	title := "Board"
	request := webui.BoardRequest{Version: 1, Action: webui.BoardCreate, IdempotencyKey: "operation-key-01", Title: &title}
	subject := strings.Repeat("c", 64)
	receipt, err := bridge.BrowserMutate(context.Background(), subject, request)
	if err != nil || receipt.Validate() != nil || repository.createScope != authority.CreationScope || repository.createActor != authority.Actor ||
		repository.createScope == subject || repository.createActor.ID == subject {
		t.Fatalf("receipt=%+v scope=%q actor=%+v err=%v", receipt, repository.createScope, repository.createActor, err)
	}
}

func TestBrowserWorkboardAuthorityIsStableScopedAndValidated(t *testing.T) {
	secret := "stable-browser-authority-fixture"
	first, err := BrowserWorkboardAuthority(secret)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BrowserWorkboardAuthority(secret)
	if err != nil || second != first {
		t.Fatalf("second=%+v first=%+v err=%v", second, first, err)
	}
	different, err := BrowserWorkboardAuthority("different-browser-authority-fixture")
	if err != nil || different == first || first.CreationScope == first.Actor.ID || strings.Contains(first.CreationScope, secret) || strings.Contains(first.Actor.ID, secret) {
		t.Fatalf("first=%+v different=%+v err=%v", first, different, err)
	}
	if _, err = BrowserWorkboardAuthority("short"); err == nil {
		t.Fatal("short authority secret accepted")
	}
	repository := &bridgeBoardRepository{}
	if _, err = NewWorkboardBridgeWithBrowserAuthority(repository, repository, time.Now,
		workboard.Authority{CreationScope: "stable-scope", Actor: workboard.Actor{ID: "worker-a", Type: "worker"}}); err == nil {
		t.Fatal("worker browser authority accepted")
	}
}

func TestWorkboardBridgeRejectsWorkerAuthorityActionsBeforeDispatch(t *testing.T) {
	repository := &bridgeBoardRepository{}
	bridge, err := NewWorkboardBridge(repository, repository, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	revision := int64(1)
	request := webui.BoardRequest{Version: 1, Action: webui.CardClaim, IdempotencyKey: "claim-operation-key-01",
		BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}
	_, err = bridge.NativeMutate(context.Background(), request)
	if err == nil || repository.control.Kind != "" || repository.evaluation.Kind != "" {
		t.Fatalf("worker lifecycle command reached operator bridge: %v", err)
	}
}

func TestWorkboardBridgeComposesOnlyOperatorControlAndAcceptanceAuthority(t *testing.T) {
	repository := &bridgeBoardRepository{}
	authority, err := BrowserWorkboardAuthority("stable-browser-authority-fixture")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridgeWithBrowserAuthority(repository, repository, time.Now, authority)
	if err != nil {
		t.Fatal(err)
	}
	revision := int64(2)
	pause := webui.BoardRequest{Version: 1, Action: webui.CardPauseRequest, IdempotencyKey: "pause-operation-key-01",
		BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}
	if _, err = bridge.NativeMutate(context.Background(), pause); err == nil || repository.control.Actor.Type != "operator" || repository.control.Actor.ID != "api_operator" {
		t.Fatalf("pause authority=%+v err=%v", repository.control.Actor, err)
	}
	resume := pause
	resume.Action, resume.IdempotencyKey = webui.CardResumeRequest, "resume-operation-key-01"
	if _, err = bridge.NativeMutate(context.Background(), resume); err == nil || repository.control.Kind != workboard.ControlResumeRequest ||
		repository.control.Actor.Type != "operator" || repository.control.ExpectedCardRevision != revision {
		t.Fatalf("resume control=%+v err=%v", repository.control, err)
	}
	digest := strings.Repeat("a", 64)
	accept := webui.BoardRequest{Version: 1, Action: webui.AcceptanceAccept, IdempotencyKey: "accept-operation-key-1",
		BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", CandidateID: "candidate-a",
		ExpectedCardRevision: &revision, CriteriaRevision: &revision, EvidenceHeadRevision: &revision,
		CandidateDigest: digest, CriteriaDigest: digest, EvidenceSetDigest: digest, PolicyDigest: digest, Evidence: "operator accepted"}
	subject := strings.Repeat("c", 64)
	if _, err = bridge.BrowserMutate(context.Background(), subject, accept); err == nil || repository.evaluation.Actor != authority.Actor ||
		repository.evaluation.DecisionAuthorityID != authority.CreationScope || repository.evaluation.Actor.ID == subject ||
		repository.evaluation.DecisionAuthorityID == subject {
		t.Fatalf("acceptance authority=%+v scope=%q err=%v", repository.evaluation.Actor, repository.evaluation.DecisionAuthorityID, err)
	}
}
