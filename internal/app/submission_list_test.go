package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func TestListSubmissionsMissingDatabaseIsReadOnly(t *testing.T) {
	s := submissionService(t)
	page, err := s.ListSubmissions(context.Background(), submissions.ListOptions{Limit: 25})
	if err != nil || page.Version != 1 || len(page.Items) != 0 || page.HasMore || page.NextCursor != "" {
		t.Fatal(page, err)
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("listing created storage", err)
	}
	if _, err := s.ListSubmissions(context.Background(), submissions.ListOptions{Limit: 101}); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestListSubmissionsDiscoversQueuedAndCanceledWork(t *testing.T) {
	s := submissionService(t)
	ctx := context.Background()
	first, err := s.Submit(ctx, "first-request-key", Request{ModelID: "fixture", Prompt: "private first prompt"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Submit(ctx, "second-request-key", Request{ModelID: "fixture", Prompt: "private second prompt"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListSubmissions(ctx, submissions.ListOptions{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID || !page.HasMore || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	if _, err := s.CancelSubmission(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	next, err := s.ListSubmissions(ctx, submissions.ListOptions{Limit: 1, After: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != second.ID || next.Items[0].State != "canceled" || next.HasMore {
		t.Fatal(next, err)
	}
	filtered, err := s.ListSubmissions(ctx, submissions.ListOptions{Limit: 25, State: "queued"})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != first.ID {
		t.Fatal(filtered, err)
	}
}
