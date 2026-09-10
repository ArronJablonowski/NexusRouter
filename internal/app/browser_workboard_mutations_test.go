package app

import (
	"context"
	"database/sql"
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
	if _, err = raw.Exec(`DROP TABLE legacy_browser_workboard_operations; DROP TABLE browser_operation_recoveries; PRAGMA user_version=37`); err != nil {
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
		EventCount: 1, TransactionBytes: 128, BoardRevision: 1, Outcome: "committed", CreatedAt: now}}
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
