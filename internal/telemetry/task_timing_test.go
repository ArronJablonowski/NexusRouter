package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"reflect"
	"testing"
	"time"
)

func timingEvent(task string, seq int64, kind runtime.Kind, at time.Time) runtime.Event {
	return runtime.Event{Version: 1, ID: task + "-" + string(kind), TaskID: task, SessionID: task, CorrelationID: task, Sequence: seq, Kind: kind, Time: at}
}
func readTiming(t *testing.T, s *Store, task string) taskTiming {
	t.Helper()
	var p taskTiming
	if err := s.db.QueryRow(`SELECT started_at,terminal_event_id,terminal_sequence,state,duration_ns,reason FROM task_timings WHERE task_id=?`, task).Scan(&p.started, &p.terminal, &p.sequence, &p.state, &p.duration, &p.reason); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTaskTimingTerminalProjection(t *testing.T) {
	for _, mode := range []string{"completed", "failed", "canceled", "zero", "backwards", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			start := time.Date(2026, 1, 1, 0, 0, 0, 123, time.FixedZone("offset", 3600))
			end := start.Add(125 * time.Millisecond)
			kind := runtime.TaskCompleted
			switch mode {
			case "failed":
				kind = runtime.TaskFailed
			case "canceled":
				kind = runtime.TaskCanceled
			case "zero":
				end = start
			case "backwards":
				end = start.Add(-time.Nanosecond)
			case "overflow":
				end = start.AddDate(400, 0, 0)
			}
			a := timingEvent("task", 1, runtime.TaskStarted, start)
			z := timingEvent("task", 2, kind, end)
			if err := s.Append(ctx, 0, a); err != nil {
				t.Fatal(err)
			}
			pending := readTiming(t, s, "task")
			if pending.validate() != nil || pending.reason != "pending" || pending.started.String != start.UTC().Format(time.RFC3339Nano) {
				t.Fatal(pending)
			}
			if err := s.Append(ctx, 1, z); err != nil {
				t.Fatal(err)
			}
			p := readTiming(t, s, "task")
			if p.validate() != nil || p.terminal.String != z.ID || p.sequence.Int64 != 2 {
				t.Fatal(p)
			}
			if mode == "backwards" || mode == "overflow" {
				if p.reason != "invalid_time" || p.duration.Valid {
					t.Fatal(p)
				}
			} else if p.reason != "observed" || p.duration.Int64 != int64(end.Sub(start)) {
				t.Fatal(p)
			}
		})
	}
}

func TestTaskTimingProjectionFailureRollsBackJournal(t *testing.T) {
	for _, mode := range []string{"start-trigger", "terminal-trigger", "corrupt-start"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			a := timingEvent("task", 1, runtime.TaskStarted, time.Now().UTC())
			z := timingEvent("task", 2, runtime.TaskFailed, a.Time.Add(time.Second))
			if mode == "start-trigger" {
				if _, err := s.db.Exec(`CREATE TRIGGER deny_timing BEFORE INSERT ON task_timings BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
					t.Fatal(err)
				}
				if s.Append(ctx, 0, a) == nil {
					t.Fatal("ignored timing failure")
				}
				var n int
				if s.db.QueryRow(`SELECT count(*) FROM task_heads`).Scan(&n) != nil || n != 0 {
					t.Fatal(n)
				}
				return
			}
			if err := s.Append(ctx, 0, a); err != nil {
				t.Fatal(err)
			}
			if mode == "terminal-trigger" {
				if _, err := s.db.Exec(`CREATE TRIGGER deny_timing BEFORE UPDATE ON task_timings BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.db.Exec(`UPDATE task_timings SET started_at=?`, a.Time.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
			}
			before := readTiming(t, s, "task")
			if s.Append(ctx, 1, z) == nil {
				t.Fatal("corrupt timing accepted")
			}
			after := readTiming(t, s, "task")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial timing mutation")
			}
			events, err := s.Read(ctx, "task", 0, 10)
			if err != nil || len(events) != 1 {
				t.Fatal(events, err)
			}
		})
	}
}

func TestTaskTimingOrphanAppendAtomic(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	a := timingEvent("orphan", 1, runtime.TaskStarted, time.Now().UTC())
	if err := s.Append(ctx, 0, a); err != nil {
		t.Fatal(err)
	}
	z := timingEvent("orphan", 2, runtime.TaskFailed, a.Time.Add(time.Second))
	plan := sessions.InterruptionRecovery{ParentTaskID: a.TaskID, ExpectedSequence: 1, Events: []runtime.Event{z}}
	for _, commit := range []bool{false, true} {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = appendOrphanFailure(ctx, tx, plan); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		p := readTiming(t, s, a.TaskID)
		if commit {
			if p.reason != "observed" || p.duration.Int64 != int64(time.Second) || p.terminal.String != z.ID {
				t.Fatal(p)
			}
		} else if p.reason != "pending" {
			t.Fatal("rollback retained timing", p)
		}
	}
}

func TestTaskTimingSubmissionRecoveryAtomic(t *testing.T) {
	s, _ := submissionStore(t)
	ctx := context.Background()
	job := queuedSubmission(t, s, "job")
	claim := claimSubmission(t, s)
	interruptedTree(t, s, claim)
	before := readTiming(t, s, "parent")
	if _, err := s.db.Exec(`CREATE TRIGGER deny_parent_timing BEFORE UPDATE ON task_timings WHEN NEW.task_id='parent' BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(2 * time.Minute)
	if ok, err := s.RecoverInterruptedDelegation(ctx, job.ID, submitDigest("config"), now); err == nil || ok {
		t.Fatal(ok, err)
	}
	if !reflect.DeepEqual(before, readTiming(t, s, "parent")) {
		t.Fatal("partial recovery timing")
	}
	if _, err := s.db.Exec(`DROP TRIGGER deny_parent_timing`); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RecoverInterruptedDelegation(ctx, job.ID, submitDigest("config"), now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	p := readTiming(t, s, "parent")
	if p.reason != "observed" || p.state != "failed" || p.sequence.Int64 != 6 {
		t.Fatal(p)
	}
}

func TestTaskTimingMigrationEpochAndMissingStart(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	a := timingEvent("old-running", 1, runtime.TaskStarted, time.Now().UTC())
	if err := s.Append(ctx, 0, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=28`); err != nil {
		t.Fatal(err)
	}
	raw := workflowSourceRawBodies(t, s)
	m, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if !reflect.DeepEqual(raw, workflowSourceRawBodies(t, m)) {
		t.Fatal("migration changed bodies")
	}
	var n int
	if m.db.QueryRow(`SELECT count(*) FROM task_timings`).Scan(&n) != nil || n != 0 {
		t.Fatal("retrospective backfill", n)
	}
	var epoch string
	if err = m.db.QueryRow(`SELECT started_at FROM task_timing_metadata WHERE singleton=1 AND version=1`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if at, err := time.Parse(time.RFC3339Nano, epoch); err != nil || at.UTC().Format(time.RFC3339Nano) != epoch {
		t.Fatal(epoch, err)
	}
	if err = m.Append(ctx, 1, timingEvent(a.TaskID, 2, runtime.TaskFailed, a.Time.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	p := readTiming(t, m, a.TaskID)
	if p.reason != "missing_start" || p.started.Valid || p.duration.Valid {
		t.Fatal(p)
	}
	again, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	var unchanged string
	if again.db.QueryRow(`SELECT started_at FROM task_timing_metadata`).Scan(&unchanged) != nil || unchanged != epoch {
		t.Fatal("epoch reset")
	}
}

func TestTaskTimingFailedMigrationRollsBack(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; CREATE TABLE task_timings(sentinel TEXT); DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=28`); err != nil {
		t.Fatal(err)
	}
	if bad, err := Open(ctx, path); err == nil {
		bad.Close()
		t.Fatal("conflicting timing table accepted")
	}
	var version int
	if s.db.QueryRow(`PRAGMA user_version`).Scan(&version) != nil || version != 28 {
		t.Fatal(version)
	}
	var epoch string
	if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE name='task_timing_metadata'`).Scan(&epoch); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("partial metadata", epoch, err)
	}
	if _, err := s.db.Exec(`DROP TABLE task_timings`); err != nil {
		t.Fatal(err)
	}
	m, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
}
