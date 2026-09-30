package telemetry

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestWorkflowScanGuardFailuresNeverPersist(t *testing.T) {
	for _, kind := range []string{"denied", "panic", "canceled", "deadline", "nil"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			workflowSourceFixture(t, s, "task", "session", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
			calls := 0
			guard := func(ctx context.Context, _ skills.WorkflowScanPage) error {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 3*time.Second {
					t.Error("unbounded guard")
				}
				switch kind {
				case "panic":
					panic("private-secret")
				case "canceled":
					cancel()
					return nil
				case "deadline":
					<-ctx.Done()
					return nil
				}
				return errors.New("private-secret")
			}
			if kind == "nil" {
				guard = nil
			}
			got, err := s.AdvanceWorkflowScanGuarded(ctx, "project", "scan", "creative", 0, 1, guard)
			if err == nil || strings.Contains(err.Error(), "private-secret") || !reflect.DeepEqual(got, skills.WorkflowScanPage{}) {
				t.Fatal("guard failure exposed outcome", got, err)
			}
			want := 1
			if kind == "nil" {
				want = 0
			}
			if calls != want {
				t.Fatal(calls, want)
			}
			for _, table := range []string{"workflow_scans", "workflow_scan_pages"} {
				var n int
				if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
					t.Fatal("guard failure wrote", table, n, err)
				}
			}
		})
	}
}

func TestWorkflowScanGuardOwnsSnapshotAndRechecksHistoricalPage(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	workflowSourceFixture(t, s, "task", "session", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
	var retained []skills.WorkflowCandidate
	calls := 0
	guard := func(_ context.Context, p skills.WorkflowScanPage) error {
		calls++
		retained = p.Page.Candidates
		p.Scan.Name = "mutated"
		p.Page.Candidates[0].TaskID = "mutated"
		return nil
	}
	first, err := s.AdvanceWorkflowScanGuarded(ctx, "project", "scan", "creative", 0, 1, guard)
	if err != nil || calls != 1 || first.Scan.Name != "scan" || first.Page.Candidates[0].TaskID != "task" {
		t.Fatal("callback mutated committed value", first, err)
	}
	retained[0].TaskID = "retained-mutation"
	second, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "creative", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.WorkflowScan(ctx, "project", "scan")
	if err != nil || before != second.Scan {
		t.Fatal(before, err)
	}
	denied := func(_ context.Context, p skills.WorkflowScanPage) error {
		calls++
		if !reflect.DeepEqual(p, first) {
			t.Error("retry guard did not receive saved page")
		}
		return errors.New("private-denial")
	}
	if got, err := s.AdvanceWorkflowScanGuarded(ctx, "project", "scan", "creative", 0, 1, denied); err == nil || !reflect.DeepEqual(got, skills.WorkflowScanPage{}) {
		t.Fatal("historical guard ignored", got, err)
	}
	if calls != 2 {
		t.Fatal("historical guard not called once", calls)
	}
	after, err := s.WorkflowScan(ctx, "project", "scan")
	if err != nil || after != before {
		t.Fatal("historical denial moved cursor", after, err)
	}
	retry, err := s.AdvanceWorkflowScanGuarded(ctx, "project", "scan", "creative", 0, 1, guard)
	if err != nil || calls != 3 || !reflect.DeepEqual(retry, first) {
		t.Fatal("historical mutation escaped", retry, err)
	}
}

func TestWorkflowScanGuardInvalidInputDoesNotCall(t *testing.T) {
	s, _ := generationStore(t)
	calls := 0
	guard := func(context.Context, skills.WorkflowScanPage) error { calls++; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if _, err := s.AdvanceWorkflowScanGuarded(ctx, "project", "scan", "creative", 0, 1, guard); err == nil {
			t.Fatal("invalid context accepted")
		}
	}
	if _, err := s.AdvanceWorkflowScanGuarded(context.Background(), "project", "scan", "creative", 0, 0, guard); err == nil {
		t.Fatal("invalid limit accepted")
	}
	if calls != 0 {
		t.Fatal("guard called before admission")
	}
}
