package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestScopeLeaseStatusReadOnly(t *testing.T) {
	s, path, req := approvalFixture(t)
	now := req.CreatedAt.Add(time.Second)
	if _, err := s.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('other-task','other-session',1,'running')`); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct {
		task, scope string
		writer      int
		expiry      time.Time
	}{
		{req.TaskID, "workspace", 0, now.Add(time.Hour)}, {req.TaskID, "create_a", 1, now},
		{"other-task", "create_b", 0, now}, {"other-task", "create_c", 1, now.Add(time.Hour)},
		{req.TaskID, "unrelated", 0, now}, {req.TaskID, "workspace", 0, now},
	} {
		insertObservedLease(t, s, fmt.Sprint("private-token-", i), row.task, row.scope, row.writer, row.expiry.UnixNano())
	}
	if _, err := s.db.Exec(`UPDATE resource_leases SET released=1 WHERE token='private-token-5'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"workspace", "create_unseen"} {
		got, err := r.ScopeLeaseStatus(context.Background(), scope, now)
		if err != nil || got.Validate() != nil || len(got.Holders) != 2 {
			t.Fatal(got, err)
		}
		byTask := map[string]workers.ScopeLeaseHolder{}
		for _, h := range got.Holders {
			byTask[h.TaskID] = h
		}
		if byTask[req.TaskID] != (workers.ScopeLeaseHolder{TaskID: req.TaskID, LiveReaders: 1, ExpiredWriters: 1}) || byTask["other-task"] != (workers.ScopeLeaseHolder{TaskID: "other-task", ExpiredReaders: 1, LiveWriters: 1}) {
			t.Fatal(got)
		}
		body, _ := json.Marshal(got)
		for _, secret := range []string{"private-token", "private-owner", "create_a", "create_b", "create_c", "process_id"} {
			if strings.Contains(string(body), secret) {
				t.Fatal("metadata leaked", string(body))
			}
		}
	}
	for _, scope := range []string{"unrelated", "absent"} {
		got, err := r.ScopeLeaseStatus(context.Background(), scope, now)
		if err != nil || got.Validate() != nil || len(got.Holders) != map[string]int{"unrelated": 1, "absent": 0}[scope] {
			t.Fatal(got, err)
		}
	}
	r.Close()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("database mutated", err)
	}
}

func TestScopeLeaseStatusLimit(t *testing.T) {
	s, _, req := approvalFixture(t)
	now := req.CreatedAt
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if _, err := tx.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires) VALUES(?,?,'owner','scope',0,?)`, fmt.Sprint("token-", i), req.TaskID, now.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := s.ScopeLeaseStatus(context.Background(), "scope", now)
	if err != nil || len(got.Holders) != 1 || got.Holders[0].ExpiredReaders != 1000 {
		t.Fatal(got, err)
	}
	insertObservedLease(t, s, "overflow", req.TaskID, "scope", 0, now.UnixNano())
	if got, err := s.ScopeLeaseStatus(context.Background(), "scope", now); err == nil || !reflect.DeepEqual(got, workers.ScopeLeaseStatus{}) {
		t.Fatal(got, err)
	}
}

func TestScopeLeaseStatusCorruption(t *testing.T) {
	for name, statement := range map[string]string{
		"owner": `UPDATE resource_leases SET owner=char(10)`, "token": `UPDATE resource_leases SET token=''`,
		"expiry": `UPDATE resource_leases SET expires=9223372036854775807`, "writer": `PRAGMA ignore_check_constraints=ON; UPDATE resource_leases SET writer=2`,
		"head": `UPDATE task_heads SET state='invalid'`, "session": `UPDATE task_heads SET session_id=''`, "sequence": `UPDATE task_heads SET sequence=0`,
		"missing-head": `PRAGMA foreign_keys=OFF; DELETE FROM task_heads`, "schema": fmt.Sprintf(`PRAGMA user_version=%d`, currentStorageSchema+1),
		"duplicate-writer": `UPDATE resource_leases SET writer=1; INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires) SELECT 'second',task_id,owner,scope,1,expires FROM resource_leases`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _, req := approvalFixture(t)
			insertObservedLease(t, s, "token", req.TaskID, "scope", 0, req.CreatedAt.UnixNano())
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			got, err := s.ScopeLeaseStatus(context.Background(), "scope", req.CreatedAt)
			if err == nil || !reflect.DeepEqual(got, workers.ScopeLeaseStatus{}) {
				t.Fatal(got, err)
			}
		})
	}
}

func TestScopeLeaseStatusLegacyAndInputs(t *testing.T) {
	for _, schema := range []int{1, 2, 3, 21, 22, 23} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			s, path, req := approvalFixture(t)
			if schema < 23 {
				if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries`); err != nil {
					t.Fatal(err)
				}
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
			got, err := r.ScopeLeaseStatus(context.Background(), "scope", req.CreatedAt)
			if err != nil || got.Validate() != nil || got.Available != (schema >= 3) || len(got.Holders) != 0 || got.StorageSchema != schema {
				t.Fatal(got, err)
			}
		})
	}
	s, _, req := approvalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, input := range []struct {
		ctx   context.Context
		scope string
		now   time.Time
	}{{nil, "scope", req.CreatedAt}, {ctx, "scope", req.CreatedAt}, {context.Background(), "", req.CreatedAt}, {context.Background(), "scope", time.Time{}}} {
		if _, err := s.ScopeLeaseStatus(input.ctx, input.scope, input.now); err == nil {
			t.Fatal("invalid accepted")
		}
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if _, err := OpenReadOnly(context.Background(), missing); err == nil {
		t.Fatal("missing opened")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("missing created", err)
	}
}
