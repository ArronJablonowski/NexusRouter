package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/webui"
)

func TestWorkboardBridgePersistsRichCardThroughNativeContract(t *testing.T) {
	ctx := context.Background()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "workboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Add(-time.Second)
	bridge, err := NewWorkboardBridge(store, store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	title := "Delivery board"
	boardReceipt, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
		IdempotencyKey: "bridge-board-key-0001", Title: &title})
	if err != nil || boardReceipt.Validate() != nil {
		t.Fatalf("board receipt=%+v err=%v", boardReceipt, err)
	}
	cardTitle := "Wire the Kanban"
	criteria := []webui.AcceptanceCriterion{{Version: 1, ID: "criterion-1", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Focused tests pass", Required: true}}
	budget := &webui.WorkBudget{AttemptLimit: 3, TimeLimitMS: 60_000, TokenLimit: 100_000, CostMicros: 50_000}
	cardReceipt, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCreate,
		IdempotencyKey: "bridge-card-key-00001", BoardID: boardReceipt.BoardID, Title: &cardTitle,
		Criteria: criteria, Budget: budget, ExpectedBoardRevision: revisionPointer(boardReceipt.BoardRevision), ExpectedGraphRevision: revisionPointer(1)})
	if err != nil || cardReceipt.Validate() != nil || cardReceipt.CardID == "" {
		t.Fatalf("card receipt=%+v err=%v", cardReceipt, err)
	}
	now = time.Now().UTC().Add(time.Second)
	revisedCriteria := append(criteria, webui.AcceptanceCriterion{Version: 1, ID: "operator-review", Kind: "subjective",
		RequiredSource: "user_feedback", ValidatorID: "operator", Description: "Operator accepts the result", Required: true})
	criteriaReceipt, err := bridge.BrowserMutate(ctx, strings.Repeat("a", 64), webui.BoardRequest{Version: 1, Action: webui.CriteriaRevise,
		IdempotencyKey: "bridge-criteria-key-01", BoardID: boardReceipt.BoardID, CardID: cardReceipt.CardID,
		Criteria: revisedCriteria, ExpectedCardRevision: cardReceipt.CardRevision, ExpectedCriteriaRevision: revisionPointer(1)})
	if err != nil || criteriaReceipt.Validate() != nil || criteriaReceipt.CardRevision == nil || *criteriaReceipt.CardRevision != *cardReceipt.CardRevision+1 {
		t.Fatalf("criteria receipt=%+v err=%v", criteriaReceipt, err)
	}
	snapshot, err := bridge.NativeRead(ctx, boardReceipt.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || snapshot.Validate() != nil || len(snapshot.Cards) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	card := snapshot.Cards[0]
	if card.ID != cardReceipt.CardID || card.Budget != *budget || card.CriteriaRevision != 2 || len(card.Criteria) != 2 || card.Criteria[1] != revisedCriteria[1] {
		t.Fatalf("rich card did not round trip: %+v", card)
	}
	events, err := bridge.NativeEvents(ctx, boardReceipt.BoardID, webui.BoardEventOptions{Limit: 100})
	if err != nil || events.Validate() != nil || len(events.Items) != 3 || events.Items[2].Kind != webui.CriteriaRevise || events.Items[2].CardID != cardReceipt.CardID {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func revisionPointer(value int64) *int64 { return &value }
