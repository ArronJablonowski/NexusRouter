package telemetry

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestWorkflowScanLostAckRestartEpochFence(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	workflowSourceFixture(t, s, "task-b", "session-b", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	late := workflowSourceFixture(t, s, "task-d", "session-d", "creative", runtime.TaskCompleted, evaluation.UserFeedback, false, true)
	first, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 0, 1)
	if err != nil || first.Scan.Revision != 1 || first.Scan.Epoch != 1 || first.Scan.Complete || first.Scan.Cursor != "task-b" || first.Scan.Upper != "task-d" {
		t.Fatal(first, err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	retry, err := other.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 0, 1)
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("lost ack changed result", retry, err)
	}
	// Both lower and between-cursor inserts are deferred to the next epoch.
	workflowSourceFixture(t, s, "task-a", "session-a", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	workflowSourceFixture(t, s, "task-c", "session-c", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	second, err := other.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 1, 1)
	if err != nil || !second.Scan.Complete || second.Scan.Cursor != "task-d" || second.Scan.Fence != first.Scan.Fence || len(second.Page.Candidates) != 0 {
		t.Fatal("epoch membership drift", second, err)
	}
	late.ID = "late-positive"
	late.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "positive", Passed: true}}
	if err = s.SupersedeEvaluation(ctx, "task-d-evaluation", late); err != nil {
		t.Fatal(err)
	}
	next, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 2, 20)
	if err != nil || next.Scan.Epoch != 2 || next.Scan.Revision != 3 || next.After != "" || !next.Scan.Complete || len(next.Page.Candidates) != 4 || next.Scan.Fence <= first.Scan.Fence {
		t.Fatal("new epoch omitted late work", next, err)
	}
	historical, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 0, 1)
	if err != nil || !reflect.DeepEqual(historical, first) {
		t.Fatal("historical replay advanced", historical, err)
	}
	for _, args := range []struct {
		domain string
		rev    int64
		limit  int
	}{{"other", 0, 1}, {"creative", 0, 2}, {"creative", 8, 1}} {
		if _, err = s.AdvanceWorkflowScan(ctx, "project", "scan", args.domain, args.rev, args.limit); err == nil {
			t.Fatal("conflict accepted", args)
		}
	}
}

func TestWorkflowScanConcurrentSameCAS(t *testing.T) {
	s, path := generationStore(t)
	other, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	workflowSourceFixture(t, s, "task", "session", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
	var wg sync.WaitGroup
	var pages [2]skills.WorkflowScanPage
	var errs [2]error
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			pages[i], errs[i] = store.AdvanceWorkflowScan(context.Background(), "project", "scan", "creative", 0, 1)
		}(i, store)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(pages[0], pages[1]) {
		t.Fatal(pages, errs)
	}
	head, err := s.WorkflowScan(context.Background(), "project", "scan")
	if err != nil || head.Revision != 1 || !head.Complete {
		t.Fatal(head, err)
	}
}

func TestWorkflowScanCorruptionCannotAdvance(t *testing.T) {
	for _, kind := range []string{"head", "page", "source", "mapping"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			workflowSourceFixture(t, s, "task-a", "session-a", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
			workflowSourceFixture(t, s, "task-b", "session-b", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
			if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 0, 1); err != nil {
				t.Fatal(err)
			}
			query := map[string]string{"head": `UPDATE workflow_scans SET body='{}'`, "page": `UPDATE workflow_scan_pages SET body='{}'`, "source": `UPDATE events SET body='{}' WHERE task_id='task-b' AND sequence=2`, "mapping": `DELETE FROM workflow_scan_tasks WHERE task_id='task-b'`}[kind]
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			var before string
			if err := s.db.QueryRow(`SELECT body FROM workflow_scans`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			got, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 1, 1)
			if err == nil || !reflect.DeepEqual(got, skills.WorkflowScanPage{}) {
				t.Fatal("corrupt scan advanced", got, err)
			}
			var after string
			if err := s.db.QueryRow(`SELECT body FROM workflow_scans`).Scan(&after); err != nil || after != before {
				t.Fatal("corrupt scan repaired", err)
			}
			var pages int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_scan_pages`).Scan(&pages); err != nil || pages != 1 {
				t.Fatal("partial page committed", pages, err)
			}
		})
	}
}

func TestWorkflowScanBoundsReadonlyEmpty(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	for _, n := range []int{0, 21} {
		if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 0, n); err == nil {
			t.Fatal("limit accepted")
		}
	}
	for _, revision := range []int64{-1, 1e9} {
		if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", revision, 1); err == nil {
			t.Fatal("revision accepted")
		}
	}
	if _, err := s.AdvanceWorkflowScan(nil, "project", "scan", "creative", 0, 1); err == nil {
		t.Fatal("nil context accepted")
	}
	page, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 0, 1)
	if err != nil || !page.Scan.Complete || page.Scan.Upper != "" || page.Scan.Fence != 0 {
		t.Fatal(page, err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err = ro.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 1, 1); err == nil {
		t.Fatal("read-only scan wrote")
	}
	head, err := ro.WorkflowScan(ctx, "project", "scan")
	if err != nil || head != page.Scan {
		t.Fatal(head, err)
	}
	var nilStore *Store
	if _, err = nilStore.WorkflowScan(ctx, "project", "scan"); err == nil {
		t.Fatal("nil store accepted")
	}
}

func TestWorkflowScanRejectsNoncontiguousIntegerLedger(t *testing.T) {
	for _, replacement := range []string{"0", "-1", "0.5", "3"} {
		t.Run(replacement, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			for _, revision := range []int64{0, 1} {
				if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", revision, 1); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`UPDATE workflow_scan_pages SET revision=` + replacement + ` WHERE revision=1`); err != nil {
				t.Fatal(err)
			}
			var before string
			if err := s.db.QueryRow(`SELECT body FROM workflow_scans`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := s.WorkflowScan(ctx, "project", "scan"); err == nil {
				t.Fatal("read accepted invalid ledger")
			}
			for _, revision := range []int64{1, 2} {
				got, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", revision, 1)
				if err == nil || !reflect.DeepEqual(got, skills.WorkflowScanPage{}) {
					t.Fatal("invalid ledger replayed or advanced", got, err)
				}
			}
			var after string
			if err := s.db.QueryRow(`SELECT body FROM workflow_scans`).Scan(&after); err != nil || before != after {
				t.Fatal("invalid ledger mutated", err)
			}
		})
	}
}
