package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func summaryAttemptFixture(t *testing.T, s *Store) (sessions.SummaryAttempt, *sessions.SummaryDraft) {
	t.Helper()
	ctx := context.Background()
	for i, k := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
		e := event(string(k), int64(i+1), k)
		e.TurnID, e.AttemptID = "turn", "attempt"
		if k == runtime.TaskStarted {
			e.Data.Messages = []providers.Message{{Role: "user", Content: "original request"}}
		}
		if k == runtime.TurnCompleted {
			e.Data.Text = "answer"
		}
		if err := s.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	source, err := sessions.Replay(ctx, s, "task")
	if err != nil {
		t.Fatal(err)
	}
	r := sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"preserve request"}}}
	_, checkpoint, err := sessions.PrepareContinuation(source, r)
	if err != nil {
		t.Fatal(err)
	}
	a := sessions.SummaryAttempt{Version: 1, ID: "summary-a", TaskID: "task", SourceDigest: checkpoint.SourceDigest, Model: "reviewer", Provider: "provider", Status: "started", SourceSequence: source.Sequence, Keep: 1, EstimatedCost: .01, StartedAt: time.Unix(200, 0).UTC()}
	d := &sessions.SummaryDraft{Request: r, Checkpoint: checkpoint, SourceTaskID: a.TaskID, SourceSequence: a.SourceSequence, SourceDigest: a.SourceDigest, Model: a.Model, Usage: &providers.Usage{InputTokens: 10, OutputTokens: 2}, Elapsed: time.Second}
	return a, d
}

func TestSummaryAttemptLifecycleAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "summary.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	a, d := summaryAttemptFixture(t, s)
	if err := s.BeginSummary(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSummary(ctx, a); err != nil {
		t.Fatal("retry", err)
	}
	done := a
	done.Status, done.Draft, done.FinishedAt = "drafted", d, a.StartedAt.Add(time.Second)
	if err := s.CompleteSummary(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteSummary(ctx, done); err != nil {
		t.Fatal("terminal retry", err)
	}
	failed := a
	failed.Status, failed.Code, failed.FinishedAt = "failed", "canceled", done.FinishedAt
	if err := s.FinishSummary(ctx, failed); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal rewrite", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM fitness").Scan(&count); err != nil || count != 0 {
		t.Fatal("fitness mutated", count, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.SummaryAttempt(ctx, a.ID)
	if err != nil || got.Status != "drafted" || got.Draft.Checkpoint.SourceDigest != a.SourceDigest {
		t.Fatal(got, err)
	}
	page, err := s.ListSummaryAttempts(ctx, a.TaskID, "", 1)
	if err != nil || len(page) != 1 {
		t.Fatal(page, err)
	}
	page, err = s.ListSummaryAttempts(ctx, a.TaskID, a.ID, 100)
	if err != nil || len(page) != 0 {
		t.Fatal(page, err)
	}
	if _, err := s.ListSummaryAttempts(ctx, a.TaskID, "", 101); err == nil {
		t.Fatal("unbounded list")
	}
	a.ID = "new"
	if err := s.BeginSummary(ctx, a); err == nil {
		t.Fatal("readonly write")
	}
}

func TestSummaryAttemptRejectsForgedSourceAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, d := summaryAttemptFixture(t, s)
	wrong := a
	wrong.SourceSequence++
	if err := s.BeginSummary(ctx, wrong); err == nil {
		t.Fatal("forged sequence accepted")
	}
	wrong = a
	wrong.Keep = 2
	if err := s.BeginSummary(ctx, wrong); err == nil {
		t.Fatal("no-op compaction accepted")
	}
	if err := s.BeginSummary(ctx, a); err != nil {
		t.Fatal(err)
	}
	done := a
	done.Status, done.FinishedAt, done.Draft = "drafted", a.StartedAt.Add(time.Second), d
	d.Checkpoint.BeforeContextTokens++
	if err := s.CompleteSummary(ctx, done); err == nil {
		t.Fatal("forged estimate accepted")
	}
	d.Checkpoint.BeforeContextTokens--
	d.Usage.OutputTokens = -1
	if err := s.CompleteSummary(ctx, done); err == nil {
		t.Fatal("negative usage accepted")
	}
	d.Usage.OutputTokens = 2
	if _, err := s.db.Exec(`CREATE TRIGGER fail_summary BEFORE UPDATE ON summary_attempts BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteSummary(ctx, done); err == nil {
		t.Fatal("injected failure ignored")
	}
	got, err := s.SummaryAttempt(ctx, a.ID)
	if err != nil || got.Status != "started" || got.Draft != nil {
		t.Fatal("partial draft persisted", got, err)
	}
}

func TestSummaryAttemptConcurrentTerminal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "summary.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a, d := summaryAttemptFixture(t, s)
	if err := s.BeginSummary(ctx, a); err != nil {
		t.Fatal(err)
	}
	done := a
	done.Status, done.FinishedAt, done.Draft = "drafted", a.StartedAt.Add(time.Second), d
	failed := a
	failed.Status, failed.FinishedAt, failed.Code = "failed", done.FinishedAt, "canceled"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, op := range []func() error{func() error { return s.CompleteSummary(ctx, done) }, func() error { return other.FinishSummary(ctx, failed) }} {
		wg.Add(1)
		go func(op func() error) { defer wg.Done(); results <- op() }(op)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	got, err := s.SummaryAttempt(ctx, a.ID)
	if err != nil || (got.Status == "drafted") != (got.Draft != nil) {
		t.Fatal(got, err)
	}
}

func TestSummaryAttemptMigrationAndValidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "summary.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := summaryAttemptFixture(t, s)
	if _, err := s.db.Exec("DROP INDEX events_submission_start; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE submission_recoveries; DROP TABLE submissions; DROP TABLE task_cancellations; DROP TABLE summary_review_heads; DROP TABLE summary_reviews; DROP TABLE summary_attempts; PRAGMA user_version=8;"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BeginSummary(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE summary_attempts SET body=json_set(body,'$.ID','forged')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SummaryAttempt(ctx, a.ID); err == nil {
		t.Fatal("corrupt read")
	}
	if _, err := s.ListSummaryAttempts(ctx, a.TaskID, "", 100); err == nil {
		t.Fatal("corrupt list")
	}
}

func TestSummaryAttemptAllTasksPagination(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := summaryAttemptFixture(t, s)
	if err := s.BeginSummary(ctx, a); err != nil {
		t.Fatal(err)
	}
	events, err := s.Read(ctx, a.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range events {
		e.ID, e.TaskID = "other-"+e.ID, "other-task"
		if err := s.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	b := a
	b.ID, b.TaskID = "summary-b", "other-task"
	if err := s.BeginSummary(ctx, b); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListSummaryAttempts(ctx, "", "", 1)
	if err != nil || len(page) != 1 || page[0].ID != a.ID {
		t.Fatal(page, err)
	}
	page, err = s.ListSummaryAttempts(ctx, "", a.ID, 1)
	if err != nil || len(page) != 1 || page[0].ID != b.ID || page[0].TaskID != b.TaskID {
		t.Fatal(page, err)
	}
	page, err = s.ListSummaryAttempts(ctx, a.TaskID, "", 100)
	if err != nil || len(page) != 1 || page[0].ID != a.ID {
		t.Fatal(page, err)
	}
	encoded, err := json.Marshal(page)
	if err != nil || strings.Contains(string(encoded), "original request") || strings.Contains(string(encoded), "answer") {
		t.Fatal("listing exposed source content", err)
	}
	page, err = s.ListSummaryAttempts(ctx, "", b.ID, 100)
	if err != nil || len(page) != 0 {
		t.Fatal(page, err)
	}
	for _, label := range []string{" ", "task\x00", string([]byte{255}), strings.Repeat("x", 129)} {
		if _, err := s.ListSummaryAttempts(ctx, label, "", 100); err == nil {
			t.Fatal("invalid task label")
		}
		if _, err := s.ListSummaryAttempts(ctx, "", label, 100); err == nil {
			t.Fatal("invalid cursor label")
		}
	}
	if _, err := s.ListSummaryAttempts(ctx, "", "", 101); err == nil {
		t.Fatal("unbounded all-task listing")
	}
}
