package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func admitConsumption(context.Context, skills.WorkflowScanConsumption) error { return nil }

func TestWorkflowConsumptionCrossPageRestartRetryAndEpoch(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	procedureFixture(t, s, "task-a", "", runtime.NoEffect)
	procedureFixture(t, s, "task-b", "", runtime.NoEffect)
	for rev := int64(0); rev < 2; rev++ {
		if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "code", rev, 1); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ConsumeWorkflowScan(ctx, "project", "scan", 0, admitConsumption)
	if err != nil || first.Considered != 1 || first.Eligible != 1 || len(first.Buckets) != 1 || len(first.Buckets[0].Sources) != 1 {
		t.Fatal(first, err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	second, err := other.ConsumeWorkflowScan(ctx, "project", "scan", 1, admitConsumption)
	if err != nil || second.Revision != 2 || len(second.Buckets) != 1 || len(second.Buckets[0].Sources) != 2 {
		t.Fatal(second, err)
	}
	retry, err := other.ConsumeWorkflowScan(ctx, "project", "scan", 0, admitConsumption)
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("historical retry changed", retry, err)
	}
	got, err := other.ListWorkflowScanBuckets(ctx, "project", "scan", 1, "", 20)
	if err != nil || len(got) != 1 || len(got[0].Sources) != 2 {
		t.Fatal(got, err)
	}
	if _, err = got[0].Group(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceWorkflowScan(ctx, "project", "scan", "code", 2, 1); err != nil {
		t.Fatal(err)
	}
	next, err := s.ConsumeWorkflowScan(ctx, "project", "scan", 2, admitConsumption)
	if err != nil || next.Epoch != 2 || len(next.Buckets) != 1 || len(next.Buckets[0].Sources) != 1 {
		t.Fatal("epoch mixed old sources", next, err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	head, err := ro.WorkflowScanConsumption(ctx, "project", "scan")
	if err != nil || !reflect.DeepEqual(head, next) {
		t.Fatal(head, err)
	}
	if _, err = ro.ConsumeWorkflowScan(ctx, "project", "scan", 0, admitConsumption); err == nil {
		t.Fatal("readonly writer accepted")
	}
}

func TestWorkflowConsumptionGuardAtomicOwnedAndRequiredOnRetry(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	procedureFixture(t, s, "task", "", runtime.NoEffect)
	if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "code", 0, 1); err != nil {
		t.Fatal(err)
	}
	for _, guard := range []func(context.Context, skills.WorkflowScanConsumption) error{nil, func(context.Context, skills.WorkflowScanConsumption) error { return errors.New("secret") }, func(context.Context, skills.WorkflowScanConsumption) error { panic("secret") }} {
		out, err := s.ConsumeWorkflowScan(ctx, "project", "scan", 0, guard)
		if err == nil || !reflect.DeepEqual(out, skills.WorkflowScanConsumption{}) || err.Error() == "secret" {
			t.Fatal(out, err)
		}
		var n int
		if err = s.db.QueryRow(`SELECT (SELECT count(*) FROM workflow_scan_consumers)+(SELECT count(*) FROM workflow_scan_consumptions)+(SELECT count(*) FROM workflow_scan_buckets)`).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial guard write", n, err)
		}
	}
	var retained skills.WorkflowScanConsumption
	out, err := s.ConsumeWorkflowScan(ctx, "project", "scan", 0, func(_ context.Context, r skills.WorkflowScanConsumption) error {
		retained = r
		r.Buckets[0].Tools[0] = "mutated"
		return nil
	})
	if err != nil || out.Buckets[0].Tools[0] == "mutated" {
		t.Fatal(out, err)
	}
	retained.Buckets[0].Sources[0].TaskID = "mutated"
	again, err := s.ConsumeWorkflowScan(ctx, "project", "scan", 0, admitConsumption)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal(again, err)
	}
	if _, err = s.ConsumeWorkflowScan(ctx, "project", "scan", 0, func(context.Context, skills.WorkflowScanConsumption) error { return errors.New("denied") }); err == nil {
		t.Fatal("retry bypassed guard")
	}
}

func TestWorkflowConsumptionRefreshesEvidenceAndActualToolResults(t *testing.T) {
	for _, mode := range []string{"feedback", "failed-tool", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			r := procedureFixture(t, s, "task", "", runtime.NoEffect)
			if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "code", 0, 1); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "feedback":
				r.ID = "negative"
				r.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "negative", Passed: false}}
				if err := s.SupersedeEvaluation(ctx, "task-evaluation", r); err != nil {
					t.Fatal(err)
				}
			case "failed-tool":
				events, err := s.Read(ctx, "task", 0, 20)
				if err != nil {
					t.Fatal(err)
				}
				e := events[6]
				e.Data.Code = "tool_failed"
				rewriteCanonicalEventForTest(t, s, "task", 7, func(event *runtime.Event) { *event = e })
			case "corrupt":
				if _, err := s.db.Exec(`UPDATE events SET body='{}' WHERE task_id='task' AND sequence=2`); err != nil {
					t.Fatal(err)
				}
			}
			out, err := s.ConsumeWorkflowScan(ctx, "project", "scan", 0, admitConsumption)
			if mode == "corrupt" {
				if err == nil {
					t.Fatal("corrupt source consumed")
				}
				var n int
				if e := s.db.QueryRow(`SELECT count(*) FROM workflow_scan_consumptions`).Scan(&n); e != nil || n != 0 {
					t.Fatal(n, e)
				}
				return
			}
			if err != nil || out.Considered != 1 || out.Eligible != 0 || len(out.Buckets) != 0 {
				t.Fatal("stale evidence grouped", out, err)
			}
		})
	}
}

func TestWorkflowConsumptionConcurrentCAS(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	procedureFixture(t, s, "task", "", runtime.NoEffect)
	if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "code", 0, 1); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	var out [2]skills.WorkflowScanConsumption
	var errs [2]error
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			out[i], errs[i] = store.ConsumeWorkflowScan(ctx, "project", "scan", 0, admitConsumption)
		}(i, store)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(out[0], out[1]) {
		t.Fatal(out, errs)
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM workflow_scan_consumptions`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestWorkflowConsumptionCorruptLedgerAndBindingReject(t *testing.T) {
	for _, mode := range []string{"missing-receipt", "head", "source-page", "bucket", "fractional-ledger"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			procedureFixture(t, s, "task-a", "", runtime.NoEffect)
			procedureFixture(t, s, "task-b", "", runtime.NoEffect)
			for rev := int64(0); rev < 2; rev++ {
				if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "code", rev, 1); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ConsumeWorkflowScan(ctx, "project", "scan", 0, admitConsumption); err != nil {
				t.Fatal(err)
			}
			query := map[string]string{"missing-receipt": `DELETE FROM workflow_scan_consumptions`, "head": `UPDATE workflow_scan_consumers SET body='{}'`, "source-page": `UPDATE workflow_scan_pages SET body='{}' WHERE revision=1`, "bucket": `UPDATE workflow_scan_buckets SET body='{}'`, "fractional-ledger": `UPDATE workflow_scan_consumptions SET revision=1.5`}[mode]
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ConsumeWorkflowScan(ctx, "project", "scan", 1, admitConsumption); err == nil {
				t.Fatal("corruption advanced", mode)
			}
			var n int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_scan_consumptions WHERE revision=2`).Scan(&n); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}

func TestWorkflowConsumptionCatalogDetectsUntouchedDeletionAndValidMutation(t *testing.T) {
	for _, mode := range []string{"deleted", "valid-mutation", "older-revision"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			procedureFixture(t, s, "task-a", "", runtime.NoEffect)
			procedureFixture(t, s, "task-b", "", runtime.NoEffect)
			workflowSourceFixture(t, s, "task-c", "session-c", "code", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
			var first skills.WorkflowScanConsumption
			for rev := int64(0); rev < 3; rev++ {
				if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "code", rev, 1); err != nil {
					t.Fatal(err)
				}
				r, err := s.ConsumeWorkflowScan(ctx, "project", "scan", rev, admitConsumption)
				if err != nil {
					t.Fatal(err)
				}
				if rev == 0 {
					first = r
				}
			}
			switch mode {
			case "deleted":
				if _, err := s.db.Exec(`DELETE FROM workflow_scan_buckets`); err != nil {
					t.Fatal(err)
				}
			case "valid-mutation":
				b := first.Buckets[0]
				b.Sources[0].SessionID = "invented-session"
				body, _ := json.Marshal(b)
				if _, err := s.db.Exec(`UPDATE workflow_scan_buckets SET body=?`, body); err != nil {
					t.Fatal(err)
				}
			case "older-revision":
				body, _ := json.Marshal(first.Buckets[0])
				if _, err := s.db.Exec(`UPDATE workflow_scan_buckets SET revision=1,body=?`, body); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.ListWorkflowScanBuckets(ctx, "project", "scan", 1, "", 20); err == nil || got != nil {
				t.Fatal("catalog corruption listed", got, err)
			}
		})
	}
}

func TestWorkflowConsumptionBoundsAndEmptyPage(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	if _, err := s.AdvanceWorkflowScan(ctx, "project", "scan", "code", 0, 1); err != nil {
		t.Fatal(err)
	}
	for _, rev := range []int64{-1, 1e9, 8} {
		if _, err := s.ConsumeWorkflowScan(ctx, "project", "scan", rev, admitConsumption); err == nil {
			t.Fatal("revision accepted", rev)
		}
	}
	if _, err := s.ConsumeWorkflowScan(nil, "project", "scan", 0, admitConsumption); err == nil {
		t.Fatal("nil ctx accepted")
	}
	r, err := s.ConsumeWorkflowScan(ctx, "project", "scan", 0, admitConsumption)
	if err != nil || r.Considered != 0 || r.Eligible != 0 || r.Buckets == nil || len(r.Buckets) != 0 {
		t.Fatal(r, err)
	}
	for _, limit := range []int{0, 21} {
		if _, err = s.ListWorkflowScanBuckets(ctx, "project", "scan", 1, "", limit); err == nil {
			t.Fatal("limit accepted")
		}
	}
	got, err := s.ListWorkflowScanBuckets(ctx, "project", "scan", 1, "", 20)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
