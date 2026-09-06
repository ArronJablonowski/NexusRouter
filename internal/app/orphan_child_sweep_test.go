package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// The fixture's actual process has exited by SIGKILL and its in-flight child
// request has disconnected. The production daemon must fail the interrupted
// child and worker, then resolve the parent, without executing any callback.
func qualifyOrphanChildSweep(t *testing.T, ctx context.Context, svc *Service, db *telemetry.Store, submission, parent string, journals map[string][]runtime.Event, expectedCode ...string) {
	t.Helper()
	childCode := "interrupted_model"
	if len(expectedCode) == 1 {
		childCode = expectedCode[0]
	}
	pendingRead := childCode == "interrupted_read_only_tool"
	parentLeases, err := db.InspectLeases(ctx, "delegation")
	if err != nil || len(parentLeases) != 1 || parentLeases[0].TaskID != parent {
		t.Fatal("missing interrupted parent reader", err)
	}
	workerLeases, err := db.InspectLeases(ctx, "delegation-"+parent)
	if err != nil || len(workerLeases) != 1 || workerLeases[0].Writer {
		t.Fatal("missing interrupted worker reader", err)
	}
	worker := workerLeases[0].TaskID
	child := ""
	for id, events := range journals {
		if id != parent && id != worker && events[0].Data.ParentTaskID == worker {
			child = id
		}
	}
	if child == "" {
		t.Fatal("missing interrupted child journal")
	}
	initial, err := db.TaskSnapshot(ctx, child)
	if err != nil || (!pendingRead && (!initial.InterruptedTurn || len(initial.Pending) != 0 || initial.UncertainEffects)) || (pendingRead && (initial.InterruptedTurn || len(initial.Pending) == 0 || !initial.UncertainEffects)) {
		t.Fatal("fixture did not interrupt a provider turn with no pending effects", err)
	}
	var childLeases []telemetry.Lease
	if pendingRead {
		childLeases, err = db.InspectLeases(ctx, "workspace")
		if err != nil {
			t.Fatal(err)
		}
		for _, lease := range childLeases {
			if lease.TaskID != child || lease.Writer {
				t.Fatal("unexpected pending-read owner")
			}
		}
	}
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	wait, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		p, pe := db.InspectLeases(wait, "delegation")
		w, we := db.InspectLeases(wait, "delegation-"+parent)
		if pe != nil || we != nil {
			t.Fatal("inspect recovering readers", pe, we)
		}
		childHeld := false
		if pendingRead {
			readers, err := db.InspectLeases(wait, "workspace")
			if err != nil {
				t.Fatal(err)
			}
			childHeld = len(readers) > 0
		}
		if len(p) == 0 && len(w) == 0 && !childHeld {
			break
		}
		if len(p) > 1 || len(w) > 1 || len(p) == 1 && p[0].Token != parentLeases[0].Token || len(w) == 1 && w[0].Token != workerLeases[0].Token {
			t.Fatal("unexpected reader ownership change")
		}
		select {
		case <-tick.C:
		case <-wait.Done():
			t.Fatal("daemon did not resolve interrupted child tree")
		case <-dispatcher.done:
			t.Fatal("daemon exited during interrupted child recovery")
		}
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatal("child recovery degraded daemon", err)
	}
	status, err := svc.SubmissionStatus(ctx, submission)
	if err != nil || status.State != "failed" || status.Result == nil || status.Result.TaskID != parent || status.Result.Text != "" {
		t.Fatal("interrupted child released accepted output", err)
	}
	repaired := map[string][]runtime.Event{}
	for task, before := range journals {
		page, err := db.ReadEventPage(ctx, task, 0, 100)
		if err != nil || page.HasMore || page.State != "failed" || len(page.Events) < len(before) || !reflect.DeepEqual(before, page.Events[:len(before)]) {
			t.Fatal("recovery changed source prefix or failed to terminate", task, err)
		}
		suffix := page.Events[len(before):]
		switch task {
		case child, worker:
			if pendingRead {
				plan, err := sessions.PlanInterruptedWorkerTree([][]runtime.Event{journals[worker], journals[child]}, page.Events[len(page.Events)-1].Time)
				if err != nil || plan.Child == nil {
					t.Fatal("cannot derive interrupted tool recovery", err)
				}
				want := plan.Worker.Events
				if task == child {
					want = plan.Child.Events
				}
				if !reflect.DeepEqual(suffix, want) {
					t.Fatal("noncanonical interrupted tool recovery suffix")
				}
				break
			}
			code := "worker_owner_interrupted"
			if task == child {
				code = childCode
			}
			if len(suffix) != 1 || suffix[0].Kind != runtime.TaskFailed || suffix[0].Data.Code != code || suffix[0].Data.Text != "" {
				t.Fatal("recovery invented completion or accepted output", task)
			}
		case parent:
			if len(suffix) != 2 || suffix[0].Kind != runtime.ToolCompleted || suffix[0].Data.Code != "delegation_recovered" || suffix[0].Data.Effect != runtime.NoEffect || suffix[1].Kind != runtime.TaskFailed || suffix[1].CausationID != suffix[0].ID {
				t.Fatal("parent did not receive bounded failure evidence")
			}
			var result struct {
				Error  string `json:"error"`
				Reason string `json:"reason"`
				Work   string `json:"work_task_id"`
				Child  string `json:"execution_task_id"`
			}
			if json.Unmarshal([]byte(suffix[0].Data.Text), &result) != nil || result.Error != "delegate_unavailable_or_rejected" || result.Reason != childCode || result.Work != worker || result.Child != child || strings.Contains(suffix[0].Data.Text, "untrusted_output") {
				t.Fatal("parent tool result accepted unfinished child")
			}
		}
		repaired[task] = page.Events
	}
	snapshot, err := db.TaskSnapshot(ctx, child)
	if err != nil || snapshot.State != "failed" || snapshot.InterruptedTurn != initial.InterruptedTurn || len(snapshot.Pending) != 0 || snapshot.UncertainEffects {
		t.Fatal("recovery invented completed model turn", err)
	}
	raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	receipts := map[string][]byte{}
	for _, lease := range append([]telemetry.Lease{parentLeases[0], workerLeases[0]}, childLeases...) {
		var body []byte
		if err := raw.QueryRowContext(ctx, `SELECT body FROM lease_recoveries WHERE lease_token=?`, lease.Token).Scan(&body); err != nil {
			t.Fatal("missing reader recovery receipt", err)
		}
		var receipt struct {
			Version       int    `json:"version"`
			Task          string `json:"task"`
			Sequence      int64  `json:"sequence"`
			State         string `json:"state"`
			Reason        string `json:"reason"`
			Digest        string `json:"digest"`
			ChildTask     string `json:"child_task_id"`
			ChildSequence int64  `json:"child_sequence"`
			ChildEvent    string `json:"child_event_id"`
		}
		reason := "terminal_reader_owner_unlocked"
		if lease.TaskID == worker {
			reason = "orphan_worker_owner_unlocked"
		}
		hash := sha256.Sum256([]byte(lease.Token))
		if len(body) > 2048 || json.Unmarshal(body, &receipt) != nil || receipt.Version != 1 || receipt.Task != lease.TaskID || receipt.Sequence != int64(len(repaired[lease.TaskID])) || receipt.State != "failed" || receipt.Reason != reason || receipt.Digest != hex.EncodeToString(hash[:]) || strings.Contains(string(body), lease.Token) || strings.Contains(string(body), "darwin-owner-") {
			t.Fatal("invalid or sensitive reader recovery receipt")
		}
		if lease.TaskID == worker && (receipt.ChildTask != child || receipt.ChildSequence != int64(len(repaired[child])) || receipt.ChildEvent != repaired[child][len(repaired[child])-1].ID) {
			t.Fatal("worker receipt did not bind interrupted child failure")
		}
		receipts[lease.Token] = body
	}
	history, err := db.RecoveryHistory(ctx, submission)
	if err != nil || len(history) != 1 || history[0].Reason != "interrupted_delegation" {
		t.Fatal("missing parent recovery receipt", err)
	}
	for range 2 {
		if changed, err := db.RecoverOrphanWorker(ctx, workerLeases[0].Token, time.Now().UTC()); err != nil || changed {
			t.Fatal("repeat child recovery changed state", err)
		}
		if _, n, err := db.RecoverOrphanWorkersPage(ctx, "", 32, time.Now().UTC()); err != nil || n != 0 {
			t.Fatal("repeat worker sweep changed state", err)
		}
		if _, err := (&Dispatcher{db: db}).recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
			t.Fatal(err)
		}
		if _, n, err := db.RecoverTerminalReadersPage(ctx, "", 32, time.Now().UTC()); err != nil || n != 0 {
			t.Fatal("repeat reader sweep changed state", err)
		}
	}
	for task, before := range repaired {
		page, err := db.ReadEventPage(ctx, task, 0, 100)
		if err != nil || page.HasMore || !reflect.DeepEqual(before, page.Events) {
			t.Fatal("repeat recovery changed journal", task, err)
		}
	}
	for token, before := range receipts {
		var after []byte
		if err := raw.QueryRowContext(ctx, `SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&after); err != nil || string(before) != string(after) {
			t.Fatal("repeat recovery changed receipt", err)
		}
	}
	var count int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM lease_recoveries`).Scan(&count); err != nil || count != 2+len(childLeases) {
		t.Fatal("unexpected recovery receipt count", err, count)
	}
	afterHistory, err := db.RecoveryHistory(ctx, submission)
	if err != nil || !reflect.DeepEqual(history, afterHistory) {
		t.Fatal("repeat recovery changed parent receipt", err)
	}
	// A separate fixture task proves capacity really became available without
	// continuing any failed task or accepting the interrupted child's output.
	start := runtime.Event{Version: 1, ID: "child-recovery-probe-start", TaskID: "child-recovery-probe", SessionID: "child-recovery-probe-session", CorrelationID: "child-recovery-probe", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	if err := db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	scopes := []string{"delegation", "delegation-" + parent}
	if pendingRead {
		scopes = append(scopes, "workspace")
	}
	for _, scope := range scopes {
		writer, err := db.AcquireLease(ctx, start.TaskID, "child-recovery-probe-writer", scope, true, time.Now(), time.Second)
		if err != nil {
			t.Fatal("recovered tree still blocks new writer", scope, err)
		}
		if err := db.ReleaseLease(ctx, writer.Token, writer.Owner); err != nil {
			t.Fatal(err)
		}
	}
}
