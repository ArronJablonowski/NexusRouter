package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

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
