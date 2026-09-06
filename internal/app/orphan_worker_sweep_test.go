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
)

// The real subprocess has been killed during the worker's terminal transaction.
// Its child succeeded and preliminary acceptance exists, but the worker must be
// failed, never promoted to accepted output, by the daemon's recovery chain.
func qualifyOrphanWorkerSweep(t *testing.T, ctx context.Context, svc *Service, db *telemetry.Store, raw *sql.DB, submission string, parentLease, workerLease telemetry.Lease, journals map[string][]runtime.Event) {
	t.Helper()
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
		parent, parentErr := db.InspectLeases(wait, "delegation")
		worker, workerErr := db.InspectLeases(wait, "delegation-"+parentLease.TaskID)
		if parentErr != nil || workerErr != nil {
			t.Fatal("inspect recovering leases", parentErr, workerErr)
		}
		if len(parent) == 0 && len(worker) == 0 {
			break
		}
		if len(parent) > 1 || len(worker) > 1 || len(parent) == 1 && parent[0].Token != parentLease.Token || len(worker) == 1 && worker[0].Token != workerLease.Token {
			t.Fatal("unexpected ownership change during orphan recovery")
		}
		select {
		case <-tick.C:
		case <-wait.Done():
			t.Fatal("daemon did not resolve dead worker and parent readers")
		case <-dispatcher.done:
			t.Fatal("dispatcher exited before orphan worker recovery")
		}
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatal("orphan sweep degraded dispatcher", err)
	}
	status, err := svc.SubmissionStatus(ctx, submission)
	if err != nil || status.State != "failed" || status.Result == nil || status.Result.TaskID != parentLease.TaskID || status.Result.Text != "" {
		t.Fatal("orphan recovery returned accepted parent output", err)
	}
	history, err := db.RecoveryHistory(ctx, submission)
	if err != nil || len(history) != 1 || history[0].Reason != "interrupted_delegation" {
		t.Fatal("missing parent submission recovery receipt", err)
	}
	repaired := map[string][]runtime.Event{}
	for task, before := range journals {
		page, err := db.ReadEventPage(ctx, task, 0, 100)
		if err != nil || page.HasMore || len(page.Events) < len(before) || !reflect.DeepEqual(before, page.Events[:len(before)]) {
			t.Fatal("orphan recovery rewrote source history", task, err)
		}
		switch task {
		case workerLease.TaskID:
			if page.State != "failed" || len(page.Events) != len(before)+1 {
				t.Fatal("orphan worker was not terminally failed exactly once")
			}
			end := page.Events[len(before)]
			if end.Kind != runtime.TaskFailed || end.Data.Code != "worker_owner_interrupted" || end.Data.Text != "" {
				t.Fatal("preliminary worker acceptance was released as output")
			}
		case parentLease.TaskID:
			if page.State != "failed" || len(page.Events) != len(before)+2 {
				t.Fatal("orphan parent was not repaired exactly once")
			}
			tool, end := page.Events[len(before)], page.Events[len(before)+1]
			var payload struct {
				Error  string `json:"error"`
				Reason string `json:"reason"`
				WorkID string `json:"work_task_id"`
			}
			if tool.Kind != runtime.ToolCompleted || tool.Data.Code != "delegation_recovered" || tool.Data.Effect != runtime.NoEffect || json.Unmarshal([]byte(tool.Data.Text), &payload) != nil || payload.Error != "delegate_unavailable_or_rejected" || payload.Reason != "worker_owner_interrupted" || payload.WorkID != workerLease.TaskID || strings.Contains(tool.Data.Text, "durable worker candidate") || strings.Contains(tool.Data.Text, "untrusted_output") || end.Kind != runtime.TaskFailed || end.CausationID != tool.ID {
				t.Fatal("orphan recovery exposed successful child output instead of failure evidence")
			}
		default:
			if !reflect.DeepEqual(before, page.Events) {
				t.Fatal("orphan recovery changed completed child")
			}
		}
		repaired[task] = page.Events
	}
	receipts := map[string][]byte{}
	for _, lease := range []telemetry.Lease{workerLease, parentLease} {
		var body []byte
		if err := raw.QueryRowContext(ctx, `SELECT body FROM lease_recoveries WHERE lease_token=?`, lease.Token).Scan(&body); err != nil {
			t.Fatal("missing recovery receipt", err)
		}
		var receipt struct {
			Version  int    `json:"version"`
			Task     string `json:"task"`
			Sequence int64  `json:"sequence"`
			State    string `json:"state"`
			Reason   string `json:"reason"`
			Digest   string `json:"digest"`
		}
		reason := "terminal_reader_owner_unlocked"
		if lease.Token == workerLease.Token {
			reason = "orphan_worker_owner_unlocked"
		}
		hash := sha256.Sum256([]byte(lease.Token))
		if len(body) > 2048 || json.Unmarshal(body, &receipt) != nil || receipt.Version != 1 || receipt.Task != lease.TaskID || receipt.Sequence != int64(len(repaired[lease.TaskID])) || receipt.State != "failed" || receipt.Reason != reason || receipt.Digest != hex.EncodeToString(hash[:]) || strings.Contains(string(body), lease.Token) || strings.Contains(string(body), "durable worker candidate") || strings.Contains(string(body), "darwin-owner-") {
			t.Fatal("invalid or sensitive recovery receipt")
		}
		receipts[lease.Token] = body
	}
	for range 2 {
		if changed, err := db.RecoverOrphanWorker(ctx, workerLease.Token, time.Now().UTC()); err != nil || changed {
			t.Fatal("repeat orphan recovery changed state", err)
		}
		if _, count, err := db.RecoverOrphanWorkersPage(ctx, "", 32, time.Now().UTC()); err != nil || count != 0 {
			t.Fatal("repeat orphan sweep changed state", err, count)
		}
		if _, err := (&Dispatcher{db: db}).recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
			t.Fatal(err)
		}
		if _, count, err := db.RecoverTerminalReadersPage(ctx, "", 32, time.Now().UTC()); err != nil || count != 0 {
			t.Fatal("repeat terminal reader sweep changed state", err, count)
		}
	}
	for task, before := range repaired {
		page, err := db.ReadEventPage(ctx, task, 0, 100)
		if err != nil || page.HasMore || !reflect.DeepEqual(before, page.Events) {
			t.Fatal("repeat sweep rewrote recovered history", task, err)
		}
	}
	for token, before := range receipts {
		var after []byte
		if err := raw.QueryRowContext(ctx, `SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&after); err != nil || string(before) != string(after) {
			t.Fatal("repeat recovery rewrote lease receipt", err)
		}
	}
	var count int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM lease_recoveries`).Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate or missing orphan recovery receipts", err, count)
	}
	afterHistory, err := db.RecoveryHistory(ctx, submission)
	if err != nil || !reflect.DeepEqual(history, afterHistory) {
		t.Fatal("repeat recovery rewrote submission receipt", err)
	}
	start := runtime.Event{Version: 1, ID: "orphan-probe-start", TaskID: "orphan-probe", SessionID: "orphan-probe-session", CorrelationID: "orphan-probe", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	if err := db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"delegation", "delegation-" + parentLease.TaskID} {
		writer, err := db.AcquireLease(ctx, start.TaskID, "orphan-probe-writer", scope, true, time.Now(), time.Second)
		if err != nil {
			t.Fatal("recovered orphan still blocks new writer", scope, err)
		}
		if err := db.ReleaseLease(ctx, writer.Token, writer.Owner); err != nil {
			t.Fatal(err)
		}
	}
}
