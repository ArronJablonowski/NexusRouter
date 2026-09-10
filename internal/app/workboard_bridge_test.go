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
	page          workboard.BoardPage
	events        workboard.BoardEventPage
	createReceipt workboard.OperationReceipt
	createScope   string
	createActor   workboard.Actor
	createCalls   int
	archiveErr    error
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
func (r *bridgeBoardRepository) ListWorkboardEvents(context.Context, string, workboard.BoardEventOptions) (workboard.BoardEventPage, error) {
	return r.events, nil
}
func (r *bridgeBoardRepository) GetCard(context.Context, string, string) (workboard.Card, error) {
	return workboard.Card{}, nil
}
func (r *bridgeBoardRepository) ListCards(context.Context, string, workboard.CardFilter) (workboard.CardPage, error) {
	return workboard.CardPage{}, nil
}
func (r *bridgeBoardRepository) LoadGraph(context.Context, string) (workboard.Graph, error) {
	return workboard.Graph{}, nil
}
func (r *bridgeBoardRepository) ApplyCardMutation(context.Context, workboard.CardMutation) (workboard.CardMutationResult, error) {
	return workboard.CardMutationResult{}, nil
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
	events, err := bridge.BrowserEvents(context.Background(), strings.Repeat("a", 64), board.ID, webui.BoardEventOptions{Limit: 100})
	if err != nil || events.Validate() != nil || len(events.Items) != 1 || events.Items[0].Kind != webui.BoardCreate {
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
	bridge, err := NewWorkboardBridge(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	title := "Board"
	request := webui.BoardRequest{Version: 1, Action: webui.BoardCreate, IdempotencyKey: "operation-key-01", Title: &title}
	subject := strings.Repeat("c", 64)
	receipt, err := bridge.BrowserMutate(context.Background(), subject, request)
	if err != nil || receipt.Validate() != nil || repository.createScope != subject || repository.createActor.ID != subject || repository.createActor.Type != "operator" {
		t.Fatalf("receipt=%+v scope=%q actor=%+v err=%v", receipt, repository.createScope, repository.createActor, err)
	}
}

func TestWorkboardBridgeRejectsUncomposedLifecycleWithoutRetry(t *testing.T) {
	repository := &bridgeBoardRepository{}
	bridge, err := NewWorkboardBridge(repository, repository, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	revision := int64(1)
	request := webui.BoardRequest{Version: 1, Action: webui.CardClaim, IdempotencyKey: "claim-operation-key-01",
		BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}
	_, err = bridge.NativeMutate(context.Background(), request)
	if !errors.Is(err, &workboard.Violation{Code: workboard.CodeInvalid}) {
		t.Fatalf("unsupported lifecycle command was not a typed rejection: %v", err)
	}
}
