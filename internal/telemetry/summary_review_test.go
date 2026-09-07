package telemetry

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func reviewDraftFixture(t *testing.T, s *Store) (sessions.SummaryAttempt, sessions.SummaryReview) {
	t.Helper()
	ctx := context.Background()
	a, d := summaryAttemptFixture(t, s)
	if err := s.BeginSummary(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Status, a.Draft, a.FinishedAt = "drafted", d, a.StartedAt.Add(time.Second)
	if err := s.CompleteSummary(ctx, a); err != nil {
		t.Fatal(err)
	}
	r := sessions.SummaryReview{Version: 1, ID: "review-a", AttemptID: a.ID, Decision: "approved", Note: "Checked requirements and source", Time: time.Unix(300, 0).UTC()}
	return a, r
}
func reviewedStart(a sessions.SummaryAttempt, r sessions.SummaryReview, id string) runtime.Event {
	e := event(id, 1, runtime.TaskStarted)
	e.TaskID = id
	e.Data.ParentTaskID = a.TaskID
	c := *a.Draft.Checkpoint
	c.SummaryAttemptID, c.SummaryReviewID = a.ID, r.ID
	e.Data.Compaction = &c
	return e
}

func TestSummaryReviewHistoryAndRevocationGate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reviews.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	a, r := reviewDraftFixture(t, s)
	e := reviewedStart(a, r, "continuation")
	if err := s.Append(ctx, 0, e); err == nil {
		t.Fatal("unreviewed draft admitted")
	}
	if err := s.RecordSummaryReview(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSummaryReview(ctx, r); err != nil {
		t.Fatal("review retry", err)
	}
	forged := reviewedStart(a, r, "forged")
	forged.Data.Compaction.BeforeContextTokens++
	if err := s.Append(ctx, 0, forged); err == nil {
		t.Fatal("different checkpoint admitted")
	}
	if err := s.Append(ctx, 0, e); err != nil {
		t.Fatal(err)
	}
	rejected := r
	rejected.ID, rejected.PreviousID, rejected.Decision = "review-b", r.ID, "rejected"
	if err := s.RecordSummaryReview(ctx, rejected); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSummaryReview(ctx, r); err != nil {
		t.Fatal("old exact review retry", err)
	}
	if err := s.Append(ctx, 0, e); err != nil {
		t.Fatal("durable start retry after revocation", err)
	}
	if err := s.Append(ctx, 0, reviewedStart(a, r, "revoked")); err == nil {
		t.Fatal("revoked review admitted")
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM task_heads WHERE task_id IN ('revoked','forged')").Scan(&n); err != nil || n != 0 {
		t.Fatal("rejection left task row", n, err)
	}
	stale := r
	stale.ID = "stale"
	stale.PreviousID = r.ID
	if err := s.RecordSummaryReview(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale head accepted", err)
	}
	changed := r
	changed.Note = "changed"
	if err := s.RecordSummaryReview(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("immutable review changed", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current, err := s.CurrentSummaryReview(ctx, a.ID)
	if err != nil || current.ID != rejected.ID {
		t.Fatal(current, err)
	}
	history, err := s.SummaryReviews(ctx, a.ID)
	if err != nil || len(history) != 2 || history[0].ID != r.ID || history[1].PreviousID != r.ID {
		t.Fatal(history, err)
	}
}

func TestLatestApprovedSummaryUsesCurrentNewestReviewAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "reviews.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, firstReview := reviewDraftFixture(t, s)
	if err := s.RecordSummaryReview(ctx, firstReview); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID, second.StartedAt = "summary-b", first.StartedAt.Add(time.Minute)
	second.Draft = nil
	second.Status, second.FinishedAt = "started", time.Time{}
	if err := s.BeginSummary(ctx, second); err != nil {
		t.Fatal(err)
	}
	second.Status, second.FinishedAt = "drafted", second.StartedAt.Add(time.Second)
	second.Draft = first.Draft
	if err := s.CompleteSummary(ctx, second); err != nil {
		t.Fatal(err)
	}
	secondReview := sessions.SummaryReview{Version: 1, ID: "review-b", AttemptID: second.ID, Decision: "approved", Note: "Reviewed newer exact source", Time: time.Unix(400, 0).UTC()}
	if err := s.RecordSummaryReview(ctx, secondReview); err != nil {
		t.Fatal(err)
	}
	a, r, err := s.LatestApprovedSummary(ctx, first.TaskID)
	if err != nil || a.ID != second.ID || r.ID != secondReview.ID {
		t.Fatal(a.ID, r.ID, err)
	}
	revoked := secondReview
	revoked.ID, revoked.PreviousID, revoked.Decision = "review-c", secondReview.ID, "rejected"
	revoked.Note, revoked.Time = "Revoked after discrepancy", time.Unix(500, 0).UTC()
	if err := s.RecordSummaryReview(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	a, r, err = s.LatestApprovedSummary(ctx, first.TaskID)
	if err != nil || a.ID != first.ID || r.ID != firstReview.ID {
		t.Fatal(a.ID, r.ID, err)
	}
	if _, _, err := s.LatestApprovedSummary(ctx, "bad task"); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("invalid task admitted", err)
	}
	if _, err := s.db.Exec("UPDATE summary_reviews SET body='{}' WHERE id=?", firstReview.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LatestApprovedSummary(ctx, first.TaskID); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("corrupt approved record bypassed", err)
	}
}

func TestSummaryReviewRollbackBoundAndMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reviews.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	a, r := reviewDraftFixture(t, s)
	if _, err := s.db.Exec("DROP INDEX events_submission_start; DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE submission_recoveries; DROP TABLE submissions; DROP TABLE task_cancellations; DROP TABLE summary_review_heads; DROP TABLE summary_reviews; PRAGMA user_version=9;"); err != nil {
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
	if _, err := s.db.Exec(`CREATE TRIGGER fail_review_head BEFORE INSERT ON summary_review_heads BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSummaryReview(ctx, r); err == nil {
		t.Fatal("injected failure ignored")
	}
	history, err := s.SummaryReviews(ctx, a.ID)
	if err != nil || len(history) != 0 {
		t.Fatal("orphan review", history, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_review_head"); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for i := 0; i < 100; i++ {
		r.ID, r.PreviousID = fmt.Sprintf("review-%03d", i), previous
		if err := s.RecordSummaryReview(ctx, r); err != nil {
			t.Fatal(i, err)
		}
		previous = r.ID
	}
	r.ID, r.PreviousID = "excess", previous
	if err := s.RecordSummaryReview(ctx, r); err == nil {
		t.Fatal("review bound ignored")
	}
	history, err = s.SummaryReviews(ctx, a.ID)
	if err != nil || len(history) != 100 {
		t.Fatal(len(history), err)
	}
}

func TestSummaryReviewRequiresDraftAndValidMetadata(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := summaryAttemptFixture(t, s)
	if err := s.BeginSummary(ctx, a); err != nil {
		t.Fatal(err)
	}
	r := sessions.SummaryReview{Version: 1, ID: "r", AttemptID: a.ID, Decision: "approved", Note: "reviewed", Time: time.Now()}
	if err := s.RecordSummaryReview(ctx, r); err == nil {
		t.Fatal("started attempt reviewed")
	}
	for _, mutate := range []func(*sessions.SummaryReview){func(r *sessions.SummaryReview) { r.Note = " " }, func(r *sessions.SummaryReview) { r.Decision = "allow" }, func(r *sessions.SummaryReview) { r.PreviousID = r.ID }, func(r *sessions.SummaryReview) { r.ID = "bad\n" }} {
		bad := r
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("invalid review accepted")
		}
	}
	c := *(&runtime.ContextCompaction{Version: 1, SourceTaskID: a.TaskID, SourceSequence: a.SourceSequence, SourceDigest: a.SourceDigest, RemovedMessages: 1, Summary: runtime.ContextSummary{Decisions: []string{"decision"}}})
	c.SummaryAttemptID = a.ID
	if err := c.Validate(a.TaskID); err == nil {
		t.Fatal("partial review binding")
	}
	c.SummaryReviewID = "review\n"
	if err := c.Validate(a.TaskID); err == nil {
		t.Fatal("invalid review label")
	}
}
