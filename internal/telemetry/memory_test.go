package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"darwinrouter/memory"
)

func testFact() memory.Fact {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	return memory.Fact{Version: 1, ID: "fact-a", Scope: "project-a", Revision: 1, Content: "Prefers Go", Provenance: "user:message-1", Confidence: 1, Privacy: "local_only", Created: now, Updated: now}
}

func TestMemoryRestartCorrectionPrivacyAndDelete(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	f := testFact()
	if err = s.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.PutMemory(ctx, f, 0); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := memory.Query{Scope: f.Scope, Limit: 10, Now: f.Created, LocalOnly: true}
	got, err := s.QueryMemory(ctx, q)
	if err != nil || len(got) != 1 || got[0].Provenance != f.Provenance {
		t.Fatalf("restart: %+v %v", got, err)
	}
	q.LocalOnly = false
	got, err = s.QueryMemory(ctx, q)
	if err != nil || len(got) != 0 {
		t.Fatalf("privacy leak: %+v %v", got, err)
	}
	q.LocalOnly = true
	f.Revision = 2
	f.Updated = f.Created.Add(time.Minute)
	f.Content = "Prefers Rust"
	f.Privacy = "shareable"
	if err = s.PutMemory(ctx, f, 1); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("privacy downgrade: %v", err)
	}
	f.Privacy = "local_only"
	if err = s.PutMemory(ctx, f, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.TouchMemory(ctx, f.Scope, f.ID, f.Updated); err != nil {
		t.Fatal(err)
	}
	got, err = s.QueryMemory(ctx, q)
	if err != nil || len(got) != 1 || got[0].Content != f.Content || !got[0].LastUse.Equal(f.Updated) {
		t.Fatalf("correct/touch: %+v %v", got, err)
	}
	if err = s.DeleteMemory(ctx, f.Scope, f.ID, 1); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("stale deletion: %v", err)
	}
	if err = s.DeleteMemory(ctx, f.Scope, f.ID, 2); err != nil {
		t.Fatal(err)
	}
	got, err = s.QueryMemory(ctx, q)
	if err != nil || len(got) != 0 {
		t.Fatalf("delete: %+v %v", got, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = reopened.QueryMemory(ctx, q)
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted fact survived restart: %+v %v", got, err)
	}
}

func TestMemoryExpiryScopePaginationAndLiteralSearch(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f := testFact()
	f.Expires = f.Created.Add(time.Hour)
	for _, scope := range []string{"project-a", "project-b"} {
		for _, id := range []string{"a", "b"} {
			f.Scope = scope
			f.ID = id
			if err = s.PutMemory(ctx, f, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	q := memory.Query{Scope: "project-a", Now: f.Created, Limit: 1, LocalOnly: true}
	got, err := s.QueryMemory(ctx, q)
	if err != nil || len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("page 1: %+v %v", got, err)
	}
	q.AfterID = "a"
	got, err = s.QueryMemory(ctx, q)
	if err != nil || len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("page 2: %+v %v", got, err)
	}
	q.Contains = "%"
	got, err = s.QueryMemory(ctx, q)
	if err != nil || len(got) != 0 {
		t.Fatalf("literal: %+v %v", got, err)
	}
	q.Contains = ""
	q.AfterID = ""
	q.Limit = 10
	q.Now = f.Expires
	got, err = s.QueryMemory(ctx, q)
	if err != nil || len(got) != 0 {
		t.Fatalf("expiry: %+v %v", got, err)
	}
	if err = s.TouchMemory(ctx, "project-a", "a", q.Now); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("expired touch: %v", err)
	}
	q.IncludeExpired = true
	got, err = s.QueryMemory(ctx, q)
	if err != nil || len(got) != 2 {
		t.Fatalf("export expired: %+v %v", got, err)
	}
	n, err := s.ExpireMemory(ctx, "project-a", q.Now)
	if err != nil || n != 2 {
		t.Fatalf("expire: %d %v", n, err)
	}
	q.Scope = "project-b"
	got, err = s.QueryMemory(ctx, q)
	if err != nil || len(got) != 2 {
		t.Fatalf("other scope: %+v %v", got, err)
	}
}

func TestMemoryConcurrentCorrectionAndCancellation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	f := testFact()
	if err = a.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	f.Revision = 2
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		wg.Go(func() { results <- s.PutMemory(ctx, f, 1) })
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, memory.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("ok %d conflicts %d", ok, conflicts)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = a.DeleteMemory(canceled, f.Scope, f.ID, 2); err == nil {
		t.Fatal("canceled deletion succeeded")
	}
	got, err := a.QueryMemory(ctx, memory.Query{Scope: f.Scope, Limit: 1, Now: f.Created, LocalOnly: true})
	if err != nil || len(got) != 1 || got[0].Revision != 2 {
		t.Fatalf("canceled changed state: %+v %v", got, err)
	}
}

func TestMemoryMigrationFromV3(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE task_cancellations; DROP TABLE summary_review_heads; DROP TABLE summary_reviews; DROP TABLE summary_attempts; DROP INDEX events_model_start; DROP TABLE review_attempts; DROP TABLE evaluation_revisions; DROP TABLE evaluation_heads; DROP TABLE audit_records; DROP TABLE memory_facts; PRAGMA user_version=3;"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.PutMemory(ctx, testFact(), 0); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if err = ro.PutMemory(ctx, testFact(), 0); err == nil {
		t.Fatal("readonly write succeeded")
	}
}
