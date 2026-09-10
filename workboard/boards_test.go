package workboard

import (
	"strings"
	"testing"
	"time"
)

func TestBoardDomainContracts(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	board := Board{Version: 1, ID: "board", Revision: 1, LayoutRevision: 1, EventSequence: 1, State: "active", Title: "Board", CreatedAt: now, UpdatedAt: now}
	if err := board.Validate(); err != nil {
		t.Fatal(err)
	}
	columns := canonicalDomainColumns("board")
	snapshot := BoardSnapshot{Version: 1, Board: board, Columns: columns, GraphRevision: 1, GraphDigest: strings.Repeat("a", 64)}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	badColumns := append([]Column(nil), columns...)
	badColumns[0], badColumns[1] = badColumns[1], badColumns[0]
	snapshot.Columns = badColumns
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted reordered canonical columns")
	}
	board.State, board.ActiveClaims = "archived", 1
	if err := board.Validate(); err == nil {
		t.Fatal("accepted archived board with active claim")
	}
}

func TestBoardRequestAndReceiptContracts(t *testing.T) {
	create := CreateBoardRequest{Version: 1, IdempotencyKey: "0123456789abcdef", Title: "Board"}
	if err := create.Validate(); err != nil {
		t.Fatal(err)
	}
	create.Title = ""
	if err := create.Validate(); err == nil {
		t.Fatal("accepted blank title")
	}
	archive := ArchiveBoardRequest{Version: 1, BoardID: "board", IdempotencyKey: "0123456789abcdef", ExpectedRevision: 1}
	if err := archive.Validate(); err != nil {
		t.Fatal(err)
	}
	archive.ExpectedRevision = 0
	if err := archive.Validate(); err == nil {
		t.Fatal("accepted missing archive fence")
	}
	title, description := "Revised", ""
	revise := ReviseBoardRequest{Version: 1, BoardID: "board", IdempotencyKey: "0123456789abcdef", ExpectedRevision: 1, Title: &title, Description: &description}
	if err := revise.Validate(); err != nil {
		t.Fatal(err)
	}
	revise.Title, revise.Description = nil, nil
	if err := revise.Validate(); err == nil {
		t.Fatal("accepted empty board revision")
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	receipt := OperationReceipt{Version: 1, BoardID: "board", OperationID: "0123456789abcdef", RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1, EventCount: 1, TransactionBytes: 1, BoardRevision: 1, Outcome: "committed", CreatedAt: now}
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	receipt.EventCount = 2
	if err := receipt.Validate(); err == nil {
		t.Fatal("accepted inconsistent event range")
	}
}

func TestBoardEventPageContract(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	events := []BoardEvent{
		{Version: 1, ID: "event-1", BoardID: "board", Sequence: 1, OperationID: "operation-000001", Kind: BoardCreateAction, ActorID: "operator", ActorType: "operator", CreatedAt: now},
		{Version: 1, ID: "event-2", BoardID: "board", Sequence: 2, OperationID: "operation-000002", Kind: BoardReviseAction, ActorID: "operator", ActorType: "operator", CreatedAt: now},
	}
	page := BoardEventPage{Version: 1, BoardID: "board", HighWaterSequence: 2, Items: events}
	if err := page.Validate(); err != nil {
		t.Fatal(err)
	}
	cardEvent := BoardEvent{Version: 1, ID: "event-3", BoardID: "board", Sequence: 3, OperationID: "operation-000003", Kind: CardMoveAction, ActorID: "operator", ActorType: "operator", CardID: "card", CreatedAt: now}
	if err := cardEvent.Validate(); err != nil {
		t.Fatal(err)
	}
	cardEvent.CardID = ""
	if err := cardEvent.Validate(); err == nil {
		t.Fatal("accepted card event without card id")
	}
	boardEvent := events[0]
	boardEvent.CardID = "card"
	if err := boardEvent.Validate(); err == nil {
		t.Fatal("accepted board event with card id")
	}
	page.Items[1].Sequence = 3
	if err := page.Validate(); err == nil {
		t.Fatal("accepted event gap past high-water")
	}
	if err := (BoardEventOptions{Limit: 0}).Validate(); err == nil {
		t.Fatal("accepted unbounded event page")
	}
}

func canonicalDomainColumns(boardID string) []Column {
	states := []State{Backlog, Ready, InProgress, Blocked, Review, Done, Canceled}
	titles := []string{"Backlog", "Ready", "In Progress", "Blocked", "Review", "Done", "Canceled"}
	columns := make([]Column, len(states))
	for index := range states {
		columns[index] = Column{Version: 1, ID: string(states[index]), BoardID: boardID, State: states[index], Title: titles[index], Rank: string(rune('0' + index))}
	}
	return columns
}
