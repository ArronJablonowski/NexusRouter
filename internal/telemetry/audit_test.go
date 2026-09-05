package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"darwinrouter/evaluation"
	"darwinrouter/providers"
	"darwinrouter/runtime"
)

func storedAudit() evaluation.AuditRecord {
	return evaluation.AuditRecord{Version: 1, ID: "audit-a", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "independent-reviewer", EvaluatorProvider: "review-provider", Audit: evaluation.Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: "v1", Domain: "code", Verdict: "abstain", Confidence: 0, Findings: []evaluation.AuditFinding{}}, EvidenceRefs: []string{"evidence-1"}, Usage: &providers.Usage{InputTokens: 10, OutputTokens: 2}, Elapsed: time.Second, Time: time.Unix(100, 0).UTC()}
}

func auditEvents(t *testing.T, s *Store, kinds ...runtime.Kind) {
	t.Helper()
	for i, kind := range kinds {
		e := event(string(kind), int64(i+1), kind)
		e.TurnID, e.AttemptID = "turn", "attempt"
		e.Data = runtime.Data{ModelID: "candidate", ProviderID: "candidate-provider"}
		if err := s.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditPersistenceIsolationAndReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "audit.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	r := storedAudit()
	if err := s.RecordAudit(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAudit(ctx, r); err != nil {
		t.Fatal("retry", err)
	}
	changed := r
	changed.EvaluatorModel = "changed"
	if err := s.RecordAudit(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("mutation accepted", err)
	}
	changed = r
	changed.ID = "audit-b"
	if err := s.RecordAudit(ctx, changed); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"fitness", "evaluations"} {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("audit mutated candidate evidence", count, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Audit(ctx, r.ID)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("%+v %v", got, err)
	}
	page, err := s.Audits(ctx, "task", "", 1)
	if err != nil || len(page) != 1 || page[0].ID != "audit-a" {
		t.Fatal(page, err)
	}
	page, err = s.Audits(ctx, "task", "audit-a", 100)
	if err != nil || len(page) != 1 || page[0].ID != "audit-b" {
		t.Fatal(page, err)
	}
	changed.ID = "audit-c"
	if err := s.RecordAudit(ctx, changed); err == nil {
		t.Fatal("read-only write accepted")
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, err := s.Audits(ctx, "task", "", limit); err == nil {
			t.Fatal("invalid limit accepted", limit)
		}
	}
}

func TestAuditAttributionAndTerminalState(t *testing.T) {
	for _, terminal := range []runtime.Kind{"", runtime.TaskCompleted, runtime.TaskFailed, runtime.TaskCanceled} {
		t.Run(string(terminal), func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "audit.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r := storedAudit()
			if err := s.RecordAudit(ctx, r); !errors.Is(err, evaluation.ErrAudit) {
				t.Fatal("absent task accepted", err)
			}
			kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted}
			if terminal != "" {
				kinds = append(kinds, terminal)
			}
			auditEvents(t, s, kinds...)
			err = s.RecordAudit(ctx, r)
			want := terminal == runtime.TaskCompleted || terminal == runtime.TaskFailed
			if (err == nil) != want {
				t.Fatal("terminal attribution", err)
			}
			r.ID, r.AttemptID = "wrong-attempt", "other"
			if err := s.RecordAudit(ctx, r); !errors.Is(err, evaluation.ErrAudit) {
				t.Fatal("wrong attempt accepted", err)
			}
		})
	}
}

func TestAuditReadRejectsCorruptRecord(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	r := storedAudit()
	if err := s.RecordAudit(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audit_records SET body=json_set(body,'$.ID','forged')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Audit(ctx, r.ID); !errors.Is(err, evaluation.ErrAudit) {
		t.Fatal("corrupt audit accepted", err)
	}
	if _, err := s.Audits(ctx, r.TaskID, "", 100); !errors.Is(err, evaluation.ErrAudit) {
		t.Fatal("corrupt audit page accepted", err)
	}
}

func TestAuditMigrationAndMissingCompletedTurn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TaskFailed)
	if err := s.RecordAudit(ctx, storedAudit()); !errors.Is(err, evaluation.ErrAudit) {
		t.Fatal("audit without completed output accepted", err)
	}
	if _, err := s.db.ExecContext(ctx, "DROP TABLE review_attempts; DROP TABLE evaluation_revisions; DROP TABLE evaluation_heads; DROP TABLE audit_records; PRAGMA user_version=4;"); err != nil {
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
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatal(version, err)
	}
	if audits, err := s.Audits(ctx, "task", "", 100); err != nil || len(audits) != 0 {
		t.Fatal(audits, err)
	}
}
