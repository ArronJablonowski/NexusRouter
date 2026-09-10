package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/browserauth"
	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func expectedBrowserOperationID(subject, key string) string {
	digest := sha256.Sum256([]byte(subject + "\x00" + key))
	return "op_" + hex.EncodeToString(digest[:])
}

func browserMutationEventPage(now time.Time, action workboard.BoardAction, operationID, cardID string) workboard.BoardEventPage {
	return workboard.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: 1, Items: []workboard.BoardEvent{{
		Version: 1, ID: "event-operation-0001", BoardID: "board-a", Sequence: 1, OperationID: operationID,
		Kind: action, ActorID: "browser-operator", ActorType: "operator", CardID: cardID, CreatedAt: now,
	}}}
}

func TestBrowserWorkboardReceiptCorrelationRejectsCrossActionRevisions(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	revision := int64(4)
	receipt := func(boardRevision int64, cardID string, cardRevision *int64) contract.OperationReceipt {
		return contract.OperationReceipt{Version: 1, BoardID: "board-a", OperationID: "domain-operation-0001",
			RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1,
			EventCount: 1, TransactionBytes: 128, BoardRevision: boardRevision, CardID: cardID, CardRevision: cardRevision,
			Outcome: "committed", CreatedAt: now}
	}
	cardOne, cardFour, cardFive, claimOne := int64(1), int64(4), int64(5), int64(1)
	tests := []struct {
		name    string
		request contract.BoardRequest
		receipt contract.OperationReceipt
		want    bool
	}{
		{"board create", contract.BoardRequest{Action: contract.BoardCreate}, receipt(1, "", nil), true},
		{"board create wrong revision", contract.BoardRequest{Action: contract.BoardCreate}, receipt(2, "", nil), false},
		{"board revise", contract.BoardRequest{Action: contract.BoardRevise, BoardID: "board-a", ExpectedBoardRevision: &revision}, receipt(5, "", nil), true},
		{"board revise stale receipt", contract.BoardRequest{Action: contract.BoardRevise, BoardID: "board-a", ExpectedBoardRevision: &revision}, receipt(4, "", nil), false},
		{"card create", contract.BoardRequest{Action: contract.CardCreate, BoardID: "board-a", ExpectedBoardRevision: &revision}, receipt(5, "card-a", &cardOne), true},
		{"card create wrong card revision", contract.BoardRequest{Action: contract.CardCreate, BoardID: "board-a", ExpectedBoardRevision: &revision}, receipt(5, "card-a", &cardFive), false},
		{"card revise", contract.BoardRequest{Action: contract.CardRevise, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, receipt(9, "card-a", &cardFive), true},
		{"card revise wrong card", contract.BoardRequest{Action: contract.CardRevise, BoardID: "board-a", CardID: "card-b", ExpectedCardRevision: &revision}, receipt(9, "card-a", &cardFive), false},
		{"card move", contract.BoardRequest{Action: contract.CardMove, BoardID: "board-a", CardID: "card-a", ExpectedBoardRevision: &revision, ExpectedCardRevision: &revision}, receipt(5, "card-a", &cardFive), true},
		{"card move wrong board revision", contract.BoardRequest{Action: contract.CardMove, BoardID: "board-a", CardID: "card-a", ExpectedBoardRevision: &revision, ExpectedCardRevision: &revision}, receipt(6, "card-a", &cardFive), false},
		{"card move wrong card revision", contract.BoardRequest{Action: contract.CardMove, BoardID: "board-a", CardID: "card-a", ExpectedBoardRevision: &revision, ExpectedCardRevision: &revision}, receipt(5, "card-a", &cardFour), false},
		{"card reorder", contract.BoardRequest{Action: contract.CardReorder, BoardID: "board-a", CardID: "card-a", ExpectedBoardRevision: &revision, ExpectedCardRevision: &revision}, receipt(5, "card-a", &cardFive), true},
		{"card reorder missing card revision", contract.BoardRequest{Action: contract.CardReorder, BoardID: "board-a", CardID: "card-a", ExpectedBoardRevision: &revision, ExpectedCardRevision: &revision}, receipt(5, "card-a", nil), false},
		{"card reorder claim revision", contract.BoardRequest{Action: contract.CardReorder, BoardID: "board-a", CardID: "card-a", ExpectedBoardRevision: &revision, ExpectedCardRevision: &revision}, func() contract.OperationReceipt {
			got := receipt(5, "card-a", &cardFive)
			got.ClaimRevision = &claimOne
			return got
		}(), false},
		{"dependency add", contract.BoardRequest{Action: contract.DependencyAdd, BoardID: "board-a", CardID: "card-a", DependencyID: "card-b", ExpectedCardRevision: &revision, ExpectedGraphRevision: &revision}, receipt(9, "card-a", &cardFive), true},
		{"dependency remove", contract.BoardRequest{Action: contract.DependencyRemove, BoardID: "board-a", CardID: "card-a", DependencyID: "card-b", ExpectedCardRevision: &revision, ExpectedGraphRevision: &revision}, receipt(3, "card-a", &cardFive), true},
		{"dependency wrong card revision", contract.BoardRequest{Action: contract.DependencyAdd, BoardID: "board-a", CardID: "card-a", DependencyID: "card-b", ExpectedCardRevision: &revision, ExpectedGraphRevision: &revision}, receipt(9, "card-a", &cardFour), false},
		{"dependency missing card revision", contract.BoardRequest{Action: contract.DependencyRemove, BoardID: "board-a", CardID: "card-a", DependencyID: "card-b", ExpectedCardRevision: &revision, ExpectedGraphRevision: &revision}, receipt(9, "card-a", nil), false},
		{"dependency claim revision", contract.BoardRequest{Action: contract.DependencyAdd, BoardID: "board-a", CardID: "card-a", DependencyID: "card-b", ExpectedCardRevision: &revision, ExpectedGraphRevision: &revision}, func() contract.OperationReceipt {
			got := receipt(9, "card-a", &cardFive)
			got.ClaimRevision = &claimOne
			return got
		}(), false},
		{"pause request", contract.BoardRequest{Action: contract.CardPauseRequest, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, receipt(9, "card-a", &cardFive), true},
		{"cancel request unrelated board revision", contract.BoardRequest{Action: contract.CardCancelRequest, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, receipt(2, "card-a", &cardFive), true},
		{"control wrong card revision", contract.BoardRequest{Action: contract.CardPauseRequest, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, receipt(9, "card-a", &cardFour), false},
		{"control missing card revision", contract.BoardRequest{Action: contract.CardCancelRequest, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, receipt(9, "card-a", nil), false},
		{"control claim revision", contract.BoardRequest{Action: contract.CardCancelRequest, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, func() contract.OperationReceipt {
			got := receipt(9, "card-a", &cardFive)
			got.ClaimRevision = &claimOne
			return got
		}(), false},
		{"acceptance accept", contract.BoardRequest{Action: contract.AcceptanceAccept, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, receipt(9, "card-a", &cardFive), true},
		{"acceptance reject unrelated board revision", contract.BoardRequest{Action: contract.AcceptanceReject, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, receipt(2, "card-a", &cardFive), true},
		{"acceptance wrong card revision", contract.BoardRequest{Action: contract.AcceptanceAccept, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, receipt(9, "card-a", &cardFour), false},
		{"acceptance missing card revision", contract.BoardRequest{Action: contract.AcceptanceReject, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, receipt(9, "card-a", nil), false},
		{"acceptance claim revision", contract.BoardRequest{Action: contract.AcceptanceAccept, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}, func() contract.OperationReceipt {
			got := receipt(9, "card-a", &cardFive)
			got.ClaimRevision = &claimOne
			return got
		}(), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := workboardReceiptMatchesRequest(test.receipt, test.request); got != test.want {
				t.Fatalf("correlation=%t want=%t receipt=%+v", got, test.want, test.receipt)
			}
		})
	}
}

func TestBrowserWorkboardAcceptanceReceiptsRequireExactBoundedEventRange(t *testing.T) {
	now := time.Date(2026, 9, 10, 18, 0, 0, 0, time.UTC)
	const firstSequence int64 = 7
	cardRevision, nextCardRevision := int64(7), int64(8)
	request := func(action contract.BoardAction) contract.BoardRequest {
		return contract.BoardRequest{Action: action, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &cardRevision}
	}
	event := func(sequence int64, action workboard.BoardAction, operationID, cardID string) workboard.BoardEvent {
		return workboard.BoardEvent{Version: 1, ID: "event-" + cardID + "-" + string(rune('a'+sequence-firstSequence)), BoardID: "board-a",
			Sequence: sequence, OperationID: operationID, Kind: action, ActorID: "browser-operator", ActorType: "operator", CardID: cardID, CreatedAt: now}
	}
	page := func(events ...workboard.BoardEvent) workboard.BoardEventPage {
		highWater := firstSequence - 1
		if len(events) > 0 {
			highWater = events[len(events)-1].Sequence
		}
		return workboard.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: highWater, Items: events}
	}
	receipt := func(count int) contract.OperationReceipt {
		return contract.OperationReceipt{Version: 1, BoardID: "board-a", OperationID: "domain-operation-0001",
			RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: firstSequence,
			LastSequence: firstSequence + int64(count) - 1, EventCount: count, TransactionBytes: 1024, BoardRevision: 12,
			CardID: "card-a", CardRevision: &nextCardRevision, Outcome: "committed", CreatedAt: now}
	}
	validAccept := []workboard.BoardEvent{
		event(7, workboard.AcceptanceAcceptAction, "domain-operation-0001", "card-a"),
		event(8, workboard.CardMoveAction, "domain-operation-0001", "card-b"),
		event(9, workboard.CardReviseAction, "domain-operation-0001", "card-c"),
	}
	tests := []struct {
		name    string
		action  contract.BoardAction
		receipt contract.OperationReceipt
		events  workboard.BoardEventPage
		want    bool
	}{
		{"reject exact event", contract.AcceptanceReject, receipt(1), page(event(7, workboard.AcceptanceRejectAction, "domain-operation-0001", "card-a")), true},
		{"reject multiple events", contract.AcceptanceReject, receipt(2), page(event(7, workboard.AcceptanceRejectAction, "domain-operation-0001", "card-a"), event(8, workboard.CardMoveAction, "domain-operation-0001", "card-b")), false},
		{"accept no successors", contract.AcceptanceAccept, receipt(1), page(event(7, workboard.AcceptanceAcceptAction, "domain-operation-0001", "card-a")), true},
		{"accept successor range", contract.AcceptanceAccept, receipt(3), page(validAccept...), true},
		{"accept missing event", contract.AcceptanceAccept, receipt(3), page(validAccept[:2]...), false},
		{"accept wrong primary action", contract.AcceptanceAccept, receipt(3), page(event(7, workboard.AcceptanceRejectAction, "domain-operation-0001", "card-a"), validAccept[1], validAccept[2]), false},
		{"accept wrong primary card", contract.AcceptanceAccept, receipt(3), page(event(7, workboard.AcceptanceAcceptAction, "domain-operation-0001", "card-z"), validAccept[1], validAccept[2]), false},
		{"accept foreign operation", contract.AcceptanceAccept, receipt(3), page(validAccept[0], event(8, workboard.CardMoveAction, "other-operation-0001", "card-b"), validAccept[2]), false},
		{"accept noncontiguous sequence", contract.AcceptanceAccept, receipt(3), page(validAccept[0], event(9, workboard.CardMoveAction, "domain-operation-0001", "card-b"), event(10, workboard.CardReviseAction, "domain-operation-0001", "card-c")), false},
		{"accept unsupported successor action", contract.AcceptanceAccept, receipt(3), page(validAccept[0], event(8, workboard.CardDependencyAddAction, "domain-operation-0001", "card-b"), validAccept[2]), false},
		{"accept repeats primary card", contract.AcceptanceAccept, receipt(3), page(validAccept[0], event(8, workboard.CardMoveAction, "domain-operation-0001", "card-a"), validAccept[2]), false},
		{"accept repeats successor card", contract.AcceptanceAccept, receipt(3), page(validAccept[0], event(8, workboard.CardMoveAction, "domain-operation-0001", "card-b"), event(9, workboard.CardReviseAction, "domain-operation-0001", "card-b")), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &bridgeBoardRepository{events: test.events}
			bridge, err := NewWorkboardBridge(repository, repository, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			mutations := &BrowserWorkboardMutations{bridge: bridge}
			if got := mutations.receiptMatchesRequest(context.Background(), strings.Repeat("c", 64), test.receipt, request(test.action)); got != test.want {
				t.Fatalf("correlation=%t want=%t receipt=%+v events=%+v", got, test.want, test.receipt, test.events.Items)
			}
		})
	}

	tooMany := receipt(workboard.MaxReverseFanout + 2)
	repository := &bridgeBoardRepository{}
	bridge, err := NewWorkboardBridge(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if (&BrowserWorkboardMutations{bridge: bridge}).receiptMatchesRequest(context.Background(), strings.Repeat("c", 64), tooMany, request(contract.AcceptanceAccept)) {
		t.Fatal("acceptance receipt above the bounded successor fanout accepted")
	}
	if repository.eventOptions.Limit != 0 {
		t.Fatal("oversized acceptance receipt reached event storage")
	}

	repository = &bridgeBoardRepository{events: page(validAccept...)}
	bridge, err = NewWorkboardBridge(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if !(&BrowserWorkboardMutations{bridge: bridge}).receiptMatchesRequest(context.Background(), strings.Repeat("c", 64), receipt(3), request(contract.AcceptanceAccept)) ||
		repository.eventOptions.Limit != 3 || repository.eventOptions.TailAfterSequence != firstSequence-1 {
		t.Fatalf("acceptance event query=%+v", repository.eventOptions)
	}
}

func TestBrowserWorkboardCardMutationReceiptsRequireExactEvent(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	boardRevision, cardRevision, nextCardRevision := int64(4), int64(7), int64(8)
	receipt := contract.OperationReceipt{Version: 1, BoardID: "board-a", OperationID: "domain-operation-0001",
		RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1,
		EventCount: 1, TransactionBytes: 128, BoardRevision: 5, CardID: "card-a", CardRevision: &nextCardRevision,
		Outcome: "committed", CreatedAt: now}
	request := func(action contract.BoardAction) contract.BoardRequest {
		result := contract.BoardRequest{Action: action, BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &cardRevision}
		if action == contract.DependencyAdd || action == contract.DependencyRemove {
			result.DependencyID, result.ExpectedGraphRevision = "card-b", &boardRevision
		} else if action != contract.CardPauseRequest && action != contract.CardCancelRequest {
			result.ExpectedBoardRevision = &boardRevision
		}
		return result
	}
	tests := []struct {
		name        string
		action      contract.BoardAction
		eventAction workboard.BoardAction
		operationID string
		cardID      string
		want        bool
	}{
		{"move", contract.CardMove, workboard.CardMoveAction, receipt.OperationID, receipt.CardID, true},
		{"reorder", contract.CardReorder, workboard.CardReorderAction, receipt.OperationID, receipt.CardID, true},
		{"dependency add", contract.DependencyAdd, workboard.CardDependencyAddAction, receipt.OperationID, receipt.CardID, true},
		{"dependency remove", contract.DependencyRemove, workboard.CardDependencyRemoveAction, receipt.OperationID, receipt.CardID, true},
		{"pause request", contract.CardPauseRequest, workboard.CardPauseRequestAction, receipt.OperationID, receipt.CardID, true},
		{"cancel request", contract.CardCancelRequest, workboard.CardCancelRequestAction, receipt.OperationID, receipt.CardID, true},
		{"wrong action", contract.CardMove, workboard.CardReorderAction, receipt.OperationID, receipt.CardID, false},
		{"dependency wrong action", contract.DependencyAdd, workboard.CardDependencyRemoveAction, receipt.OperationID, receipt.CardID, false},
		{"control wrong action", contract.CardPauseRequest, workboard.CardCancelRequestAction, receipt.OperationID, receipt.CardID, false},
		{"wrong operation", contract.CardMove, workboard.CardMoveAction, "other-operation-0001", receipt.CardID, false},
		{"wrong card", contract.CardMove, workboard.CardMoveAction, receipt.OperationID, "card-b", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &bridgeBoardRepository{events: browserMutationEventPage(now, test.eventAction, test.operationID, test.cardID)}
			bridge, err := NewWorkboardBridge(repository, repository, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			mutations := &BrowserWorkboardMutations{bridge: bridge}
			if got := mutations.receiptMatchesRequest(context.Background(), strings.Repeat("c", 64), receipt, request(test.action)); got != test.want {
				t.Fatalf("correlation=%t want=%t event=%+v", got, test.want, repository.events.Items)
			}
			if repository.eventOptions.Limit != 1 || repository.eventOptions.TailAfterSequence != 0 {
				t.Fatalf("event options=%+v", repository.eventOptions)
			}
		})
	}
}

func newBrowserTestSubject(t *testing.T) string {
	t.Helper()
	store, err := browserauth.New(browserauth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Approve(challenge.ID, challenge.DisplayCode); err != nil {
		t.Fatal(err)
	}
	session, err := store.Consume(challenge.ID, challenge.Cookie)
	if err != nil {
		t.Fatal(err)
	}
	subject, ok := store.Subject(session.Token)
	if !ok {
		t.Fatal("new browser session has no subject")
	}
	return subject
}

func TestBrowserWorkboardMutationRecoversSchema37PendingWithLegacyInitiatorAuthority(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := browserops.Open(ctx, path)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	initiator := newBrowserTestSubject(t)
	legacyAuthority := workboard.Authority{CreationScope: initiator, Actor: workboard.Actor{ID: initiator, Type: "operator"}}
	legacyBridge, err := NewWorkboardBridgeWithBrowserAuthority(store, store, time.Now, legacyAuthority)
	if err != nil {
		t.Fatal(err)
	}
	title := "Legacy delivery"
	request := contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: "legacy-restart-board-0001", Title: &title}
	mutations, err := NewBrowserWorkboardMutations(legacyBridge, journal)
	if err != nil {
		t.Fatal(err)
	}
	record, replay, err := mutations.journal.begin(ctx, initiator, string(request.Action), request.IdempotencyKey, request)
	if err != nil || replay {
		t.Fatalf("record=%+v replay=%t err=%v", record, replay, err)
	}
	first, err := legacyBridge.BrowserMutate(ctx, initiator, request)
	if err != nil {
		t.Fatal(err)
	}
	journal.Close()
	store.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`DROP TABLE legacy_browser_workboard_operations; DROP TABLE browser_operation_recoveries; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=37`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()

	reopened, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedJournal, err := browserops.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedJournal.Close()
	identity, err := reopened.WorkspaceIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workspaceAuthority, err := BrowserWorkboardAuthority(identity)
	if err != nil {
		t.Fatal(err)
	}
	workspaceBridge, err := NewWorkboardBridgeWithBrowserAuthority(reopened, reopened, time.Now, workspaceAuthority)
	if err != nil {
		t.Fatal(err)
	}
	recoveryMutations, err := NewBrowserWorkboardMutations(workspaceBridge, reopenedJournal)
	if err != nil {
		t.Fatal(err)
	}
	recoverySubject := newBrowserTestSubject(t)
	recovered, err := recoveryMutations.Mutate(ctx, recoverySubject, request)
	if err != nil || recovered != first {
		t.Fatalf("recovered=%+v first=%+v err=%v", recovered, first, err)
	}
	events, err := workspaceBridge.BrowserEvents(ctx, recoverySubject, first.BoardID, contract.BoardEventOptions{Limit: 100})
	if err != nil || len(events.Items) != 1 || events.Items[0].ActorID != initiator || events.Items[0].ActorID == recoverySubject || events.Items[0].ActorID == workspaceAuthority.Actor.ID {
		t.Fatalf("legacy attribution changed: events=%+v err=%v", events, err)
	}
	legacyRecord, found, err := reopenedJournal.Adoptable(ctx, recoverySubject, request.IdempotencyKey, string(request.Action), mustJSON(t, request))
	if err != nil || found || legacyRecord.OperationID != "" {
		t.Fatalf("terminal legacy row remained adoptable: record=%+v found=%t err=%v", legacyRecord, found, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBrowserWorkboardMutationCommitsAndReplaysFromJournal(t *testing.T) {
	ctx := context.Background()
	journal, err := browserops.Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	repository := &bridgeBoardRepository{createReceipt: workboard.OperationReceipt{Version: 1, BoardID: "board-a", OperationID: "board-operation-key-0001",
		RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1,
		EventCount: 1, TransactionBytes: 128, BoardRevision: 1, Outcome: "committed", CreatedAt: now},
		events: browserMutationEventPage(now, workboard.BoardCreateAction, "board-operation-key-0001", "")}
	bridge, err := NewWorkboardBridge(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := NewBrowserWorkboardMutations(bridge, journal)
	if err != nil {
		t.Fatal(err)
	}
	title, subject := "Delivery", strings.Repeat("c", 64)
	request := contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: "board-operation-key-0001", Title: &title}
	first, err := mutations.Mutate(ctx, subject, request)
	if err != nil || first.Validate() != nil || repository.createCalls != 1 {
		t.Fatalf("receipt=%+v calls=%d err=%v", first, repository.createCalls, err)
	}
	replayed, err := mutations.Mutate(ctx, subject, request)
	if err != nil || replayed != first || repository.createCalls != 1 {
		t.Fatalf("replayed=%+v calls=%d err=%v", replayed, repository.createCalls, err)
	}
	page, err := mutations.journal.Operations(ctx, subject, "", 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "committed" || page.Items[0].Action != string(contract.BoardCreate) ||
		page.Items[0].SubjectType != "board" || page.Items[0].SubjectID != first.BoardID {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestBrowserWorkboardMutationRejectsReceiptFromDifferentAction(t *testing.T) {
	ctx := context.Background()
	journal, err := browserops.Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	operationID := "swapped-operation-0001"
	repository := &bridgeBoardRepository{createReceipt: workboard.OperationReceipt{Version: 1, BoardID: "board-a", OperationID: operationID,
		RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1,
		EventCount: 1, TransactionBytes: 128, BoardRevision: 1, Outcome: "committed", CreatedAt: now},
		events: browserMutationEventPage(now, workboard.BoardArchiveAction, operationID, "")}
	bridge, err := NewWorkboardBridge(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := NewBrowserWorkboardMutations(bridge, journal)
	if err != nil {
		t.Fatal(err)
	}
	title, subject := "Delivery", strings.Repeat("d", 64)
	request := contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: "swapped-browser-key-0001", Title: &title}
	_, err = mutations.Mutate(ctx, subject, request)
	var operationErr *BrowserOperationError
	if !errors.As(err, &operationErr) || !errors.Is(err, ErrBrowserMutation) || operationErr.OperationID != expectedBrowserOperationID(subject, request.IdempotencyKey) {
		t.Fatalf("cross-action receipt accepted: %#v", err)
	}
	records, _, listErr := journal.List(ctx, subject, "", 10)
	if listErr != nil || len(records) != 1 || records[0].State != "pending" || repository.createCalls != 1 {
		t.Fatalf("records=%+v calls=%d err=%v", records, repository.createCalls, listErr)
	}
}

func TestBrowserWorkboardMutationJournalsDefinitiveRejection(t *testing.T) {
	ctx := context.Background()
	journal, err := browserops.Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	repository := &bridgeBoardRepository{archiveErr: &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "expected_revision"}}
	bridge, err := NewWorkboardBridge(repository, repository, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := NewBrowserWorkboardMutations(bridge, journal)
	if err != nil {
		t.Fatal(err)
	}
	revision, subject := int64(2), strings.Repeat("d", 64)
	request := contract.BoardRequest{Version: 1, Action: contract.BoardArchive, IdempotencyKey: "archive-key-0001", BoardID: "board-a", ExpectedBoardRevision: &revision}
	for attempt := 0; attempt < 2; attempt++ {
		_, mutationErr := mutations.Mutate(ctx, subject, request)
		var violation *workboard.Violation
		if !errors.As(mutationErr, &violation) || violation.Code != workboard.CodeStaleRevision || violation.Field != "expected_revision" {
			t.Fatalf("attempt %d returned %v", attempt, mutationErr)
		}
	}
	page, err := mutations.journal.Operations(ctx, subject, "", 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "rejected" || page.Items[0].SubjectType != "board" || page.Items[0].SubjectID != "board-a" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestBrowserWorkboardMutationCorrelatesNondefinitiveFailure(t *testing.T) {
	ctx := context.Background()
	journal, err := browserops.Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	privateFailure := errors.New("private bridge failure with request content")
	repository := &bridgeBoardRepository{archiveErr: privateFailure}
	bridge, err := NewWorkboardBridge(repository, repository, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := NewBrowserWorkboardMutations(bridge, journal)
	if err != nil {
		t.Fatal(err)
	}
	revision, subject := int64(2), strings.Repeat("e", 64)
	request := contract.BoardRequest{Version: 1, Action: contract.BoardArchive, IdempotencyKey: "ambiguous-archive-key-0001", BoardID: "board-a", ExpectedBoardRevision: &revision}
	_, err = mutations.Mutate(ctx, subject, request)
	var operationErr *BrowserOperationError
	wantID := expectedBrowserOperationID(subject, request.IdempotencyKey)
	if !errors.As(err, &operationErr) || operationErr.OperationID != wantID || !errors.Is(err, privateFailure) {
		t.Fatalf("operation error=%#v want_id=%q", err, wantID)
	}
	records, _, listErr := journal.List(ctx, subject, "", 10)
	if listErr != nil || len(records) != 1 || records[0].OperationID != wantID || records[0].State != "pending" || len(records[0].Response) != 0 {
		t.Fatalf("records=%+v err=%v", records, listErr)
	}
	foreign, _, listErr := journal.List(ctx, strings.Repeat("f", 64), "", 10)
	if listErr != nil || len(foreign) != 0 {
		t.Fatalf("foreign session observed operation: records=%+v err=%v", foreign, listErr)
	}
}

func TestBrowserWorkboardMutationCorrelatesJournalCommitFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "browser.db")
	journal, err := browserops.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	repository := &bridgeBoardRepository{createReceipt: workboard.OperationReceipt{Version: 1, BoardID: "board-a", OperationID: "lost-ack-domain-key-0001",
		RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1,
		EventCount: 1, TransactionBytes: 128, BoardRevision: 1, Outcome: "committed", CreatedAt: now},
		events: browserMutationEventPage(now, workboard.BoardCreateAction, "lost-ack-domain-key-0001", "")}
	bridge, err := NewWorkboardBridge(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := NewBrowserWorkboardMutations(bridge, journal)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`CREATE TRIGGER fail_browser_commit BEFORE UPDATE OF state ON browser_operations
		WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'private receipt content'); END`); err != nil {
		t.Fatal(err)
	}
	title, subject := "Sensitive board title", strings.Repeat("a", 64)
	request := contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: "lost-ack-browser-key-0001", Title: &title}
	_, err = mutations.Mutate(ctx, subject, request)
	var operationErr *BrowserOperationError
	wantID := expectedBrowserOperationID(subject, request.IdempotencyKey)
	if !errors.As(err, &operationErr) || operationErr.OperationID != wantID || !errors.Is(err, ErrBrowserMutation) || strings.Contains(err.Error(), title) || strings.Contains(err.Error(), repository.createReceipt.ResponseDigest) {
		t.Fatalf("operation error=%#v want_id=%q", err, wantID)
	}
	records, _, listErr := journal.List(ctx, subject, "", 10)
	if listErr != nil || len(records) != 1 || records[0].OperationID != wantID || records[0].State != "pending" || len(records[0].Response) != 0 || repository.createCalls != 1 {
		t.Fatalf("records=%+v calls=%d err=%v", records, repository.createCalls, listErr)
	}
}

func TestBrowserWorkboardMutationRejectsInvalidSubjectBeforeJournal(t *testing.T) {
	ctx := context.Background()
	journal, err := browserops.Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	repository := &bridgeBoardRepository{}
	bridge, err := NewWorkboardBridge(repository, repository, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := NewBrowserWorkboardMutations(bridge, journal)
	if err != nil {
		t.Fatal(err)
	}
	title := "Delivery"
	_, err = mutations.Mutate(ctx, "browser", contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: "board-operation-key-0001", Title: &title})
	if !errors.Is(err, ErrAdmission) || repository.createCalls != 0 {
		t.Fatalf("invalid authority reached domain: calls=%d err=%v", repository.createCalls, err)
	}
}

func TestBrowserWorkboardMutationRecoversAcrossRestartAndNewSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "darwin.db")
	// The authority comes from durable workspace identity, not either API token.
	firstAPIToken, rotatedAPIToken := "api-token-before-restart", "api-token-after-restart"
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := browserops.Open(ctx, path)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	workspaceIdentity, err := store.WorkspaceIdentity(ctx)
	if err != nil {
		journal.Close()
		store.Close()
		t.Fatal(err)
	}
	authority, err := BrowserWorkboardAuthority(workspaceIdentity)
	if err != nil {
		journal.Close()
		store.Close()
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridgeWithBrowserAuthority(store, store, time.Now, authority)
	if err != nil {
		journal.Close()
		store.Close()
		t.Fatal(err)
	}
	mutations, err := NewBrowserWorkboardMutations(bridge, journal)
	if err != nil {
		journal.Close()
		store.Close()
		t.Fatal(err)
	}
	title := "Restart-safe delivery"
	request := contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: "restart-board-operation-0001", Title: &title}
	firstSubject := newBrowserTestSubject(t)
	record, replay, err := mutations.journal.begin(ctx, firstSubject, string(request.Action), request.IdempotencyKey, request)
	if err != nil || replay || record.State != "pending" {
		t.Fatalf("record=%+v replay=%t err=%v", record, replay, err)
	}
	first, err := bridge.BrowserMutate(ctx, firstSubject, request)
	if err != nil || first.Validate() != nil {
		t.Fatalf("receipt=%+v err=%v", first, err)
	}
	// Simulate process loss after the domain transaction commits but before the
	// session-scoped browser operation journal can commit its receipt.
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopenedJournal, err := browserops.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedJournal.Close()
	reopenedIdentity, err := reopenedStore.WorkspaceIdentity(ctx)
	if err != nil || reopenedIdentity != workspaceIdentity || firstAPIToken == rotatedAPIToken {
		t.Fatalf("workspace identity/token fixture invalid: first=%q reopened=%q err=%v", workspaceIdentity, reopenedIdentity, err)
	}
	rotatedAuthority, err := BrowserWorkboardAuthority(reopenedIdentity)
	if err != nil || rotatedAuthority != authority {
		t.Fatalf("token rotation changed authority: first=%+v rotated=%+v err=%v", authority, rotatedAuthority, err)
	}
	reopenedBridge, err := NewWorkboardBridgeWithBrowserAuthority(reopenedStore, reopenedStore, time.Now, rotatedAuthority)
	if err != nil {
		t.Fatal(err)
	}
	reopenedMutations, err := NewBrowserWorkboardMutations(reopenedBridge, reopenedJournal)
	if err != nil {
		t.Fatal(err)
	}
	secondSubject := newBrowserTestSubject(t)
	if secondSubject == firstSubject {
		t.Fatal("replacement browser session reused ephemeral subject")
	}
	replayed, err := reopenedMutations.Mutate(ctx, secondSubject, request)
	if err != nil || replayed != first {
		t.Fatalf("replayed=%+v first=%+v err=%v", replayed, first, err)
	}
	boards, err := reopenedBridge.BrowserList(ctx, secondSubject, contract.BoardListOptions{Limit: 25})
	if err != nil || len(boards.Items) != 1 || boards.Items[0].ID != first.BoardID {
		t.Fatalf("boards=%+v err=%v", boards, err)
	}
	events, err := reopenedBridge.BrowserEvents(ctx, secondSubject, first.BoardID, contract.BoardEventOptions{Limit: 100})
	if err != nil || len(events.Items) != 1 || events.Items[0].ActorID != authority.Actor.ID ||
		events.Items[0].ActorID == firstSubject || events.Items[0].ActorID == secondSubject {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	firstJournal, err := reopenedMutations.journal.Operations(ctx, firstSubject, "", 100)
	if err != nil || len(firstJournal.Items) != 1 || firstJournal.Items[0].State != "committed" || firstJournal.Items[0].OperationID != record.OperationID {
		t.Fatalf("first journal=%+v err=%v", firstJournal, err)
	}
	secondJournal, err := reopenedMutations.journal.Operations(ctx, secondSubject, "", 100)
	if err != nil || len(secondJournal.Items) != 0 {
		t.Fatalf("second journal=%+v err=%v", secondJournal, err)
	}
	recovery, err := reopenedJournal.Recovery(ctx, record.OperationID)
	if err != nil || recovery.RecoverySubject != secondSubject || recovery.OperationID != record.OperationID || recovery.RecoveredAt.IsZero() {
		t.Fatalf("recovery=%+v err=%v", recovery, err)
	}
}
