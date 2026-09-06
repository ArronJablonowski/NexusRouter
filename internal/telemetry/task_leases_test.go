//go:build darwin || linux

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestTaskLeaseStatusReadOnly(t *testing.T) {
	s, path, req := approvalFixture(t)
	now := req.CreatedAt.Add(time.Second)
	for i := 0; i < 6; i++ {
		expiry := now.Add(time.Hour)
		if i%2 == 1 {
			expiry = now
		}
		insertObservedLease(t, s, fmt.Sprint("private-token-", i), req.TaskID, fmt.Sprint("private-scope-", i), i/2%2, expiry.UnixNano())
	}
	if _, err := s.db.Exec(`UPDATE resource_leases SET released=1,writer=CASE WHEN token='private-token-4' THEN 0 ELSE 1 END WHERE token IN ('private-token-4','private-token-5')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := r.TaskLeaseStatus(context.Background(), req.TaskID, now)
		want := workers.TaskLeaseCounts{LiveReaders: 1, ExpiredReaders: 1, LiveWriters: 1, ExpiredWriters: 1, ReleasedReaders: 1, ReleasedWriters: 1}
		if err != nil || got.Validate() != nil || got.Leases == nil || *got.Leases != want || got.Recoveries == nil || *got.Recoveries != (workers.TaskRecoveryCounts{}) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		body, _ := json.Marshal(got)
		if strings.Contains(string(body), "private-") {
			t.Fatal("private lease metadata leaked")
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("database changed", err)
	}
}

func TestTaskLeaseStatusExactLimit(t *testing.T) {
	s, _, req := approvalFixture(t)
	now := req.CreatedAt.Add(time.Second)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 1000; i++ {
		if _, err := tx.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES(?,?,?,?,0,?,0)`, fmt.Sprintf("bounded-%d", i), req.TaskID, "owner", "scope", now.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	status, err := s.TaskLeaseStatus(context.Background(), req.TaskID, now)
	if err != nil || status.Validate() != nil || status.Leases == nil || status.Leases.ExpiredReaders != 1000 || status.Recoveries == nil || *status.Recoveries != (workers.TaskRecoveryCounts{}) {
		t.Fatal("exactly 1000 records must be inspectable", status, err)
	}
}

func TestTaskLeaseStatusLegacy(t *testing.T) {
	for _, schema := range []int{1, 2, 3, 21, 22} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			s, path, req := approvalFixture(t)
			if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries`); err != nil {
				t.Fatal(err)
			}
			if schema < 22 {
				if _, err := s.db.Exec(`ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes`); err != nil {
					t.Fatal(err)
				}
			}
			if schema < 3 {
				if _, err := s.db.Exec(`DROP TABLE resource_leases`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schema)); err != nil {
				t.Fatal(err)
			}
			s.Close()
			r, err := OpenReadOnly(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			got, err := r.TaskLeaseStatus(context.Background(), req.TaskID, req.CreatedAt)
			if err != nil || got.Validate() != nil || got.StorageSchema != schema || got.Recoveries != nil || (got.Leases == nil) != (schema < 3) {
				t.Fatal(got, err)
			}
		})
	}
}

func TestTaskLeaseStatusRejectsCorruption(t *testing.T) {
	for _, mode := range []string{"expiry", "owner", "released", "overflow", "receipt", "history", "cancel", "unknown", "time"} {
		t.Run(mode, func(t *testing.T) {
			s, _, req := approvalFixture(t)
			ctx := context.Background()
			now := req.CreatedAt
			insertObservedLease(t, s, "private-token", req.TaskID, "scope", 0, now.UnixNano())
			var err error
			switch mode {
			case "expiry":
				_, err = s.db.Exec(`UPDATE resource_leases SET expires=9223372036854775807`)
			case "owner":
				_, err = s.db.Exec(`UPDATE resource_leases SET owner=char(10)`)
			case "released":
				_, err = s.db.Exec(`PRAGMA ignore_check_constraints=ON; UPDATE resource_leases SET released=2`)
			case "overflow":
				for i := 0; i < 1000; i++ {
					insertObservedLease(t, s, fmt.Sprint("token", i), req.TaskID, "scope", 0, now.UnixNano())
				}
			case "receipt":
				_, err = s.db.Exec(`INSERT INTO lease_recoveries(lease_token,digest,body) VALUES('private-token','bad','{}')`)
			case "history":
				_, err = s.db.Exec(`UPDATE events SET body='{}' WHERE sequence=1`)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "unknown":
				req.TaskID = "unknown"
			case "time":
				now = time.Time{}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.TaskLeaseStatus(ctx, req.TaskID, now)
			if err == nil || !reflect.DeepEqual(got, workers.TaskLeaseStatus{}) {
				t.Fatal(got, err)
			}
		})
	}
}

func TestTaskLeaseStatusOrphanRecoveryCounts(t *testing.T) {
	for _, mode := range []string{"completed", "active"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, mode)
			kill()
			ctx := context.Background()
			now := time.Now().UTC()
			if ok, err := s.RecoverOrphanWorker(ctx, token, now); err != nil || !ok {
				t.Fatal(ok, err)
			}
			before := orphanHistories(t, s)
			got, err := s.TaskLeaseStatus(ctx, "work", now)
			child := int64(0)
			if mode == "active" {
				child = 1
			}
			if err != nil || got.Leases == nil || got.Leases.ReleasedReaders != 1 || got.Recoveries == nil || *got.Recoveries != (workers.TaskRecoveryCounts{OrphanWorkers: 1, InterruptedChildren: child}) {
				t.Fatal(got, err)
			}
			if !reflect.DeepEqual(before, orphanHistories(t, s)) {
				t.Fatal("inspection changed journal")
			}
		})
	}
}

func TestTaskLeaseStatusValidatedRecovery(t *testing.T) {
	s, token, kill := startTerminalReaderOwner(t, "completed")
	kill()
	ctx := context.Background()
	now := time.Now().UTC()
	if ok, err := s.RecoverTerminalReader(ctx, token, now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	events, err := s.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.TaskLeaseStatus(ctx, "task", now)
	if err != nil || got.TaskState != "completed" || got.Leases.ReleasedReaders != 1 || got.Recoveries.TerminalReaders != 1 {
		t.Fatal(got, err)
	}
	after, err := s.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(events, after) || after[len(after)-1].Kind != runtime.TaskCompleted {
		t.Fatal("journal changed", err)
	}
	if _, err = s.db.Exec(`UPDATE lease_recoveries SET body='{}'`); err != nil {
		t.Fatal(err)
	}
	if got, err = s.TaskLeaseStatus(ctx, "task", now); err == nil || !reflect.DeepEqual(got, workers.TaskLeaseStatus{}) {
		t.Fatal(got, err)
	}
}
