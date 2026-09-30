package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/routing"
)

func TestObservationSetPreservesStableCorrectionIdentityAndTime(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := revisionBase(t, db)
	revision := revised(base, "operator-correction", false)
	if err = db.SupersedeEvaluation(ctx, base.ID, revision); err != nil {
		t.Fatal(err)
	}

	set, err := db.ObservationSet(ctx, base.Key)
	if err != nil || len(set.Fitness) != 2 || len(set.Advisory) != 0 || len(set.Validity) != 0 {
		t.Fatal(set, err)
	}
	first, second := set.Fitness[0], set.Fitness[1]
	if first.ID != base.ID || first.BaseID != base.ID || first.Supersedes != "" || first.Quality != 1 || !first.Time.Equal(base.Time) {
		t.Fatal("base identity changed", first)
	}
	if second.ID != revision.ID || second.BaseID != base.ID || second.Supersedes != base.ID || second.Quality != 0 || !second.Time.Equal(base.Time) {
		t.Fatal("correction identity or source time changed", second)
	}
	// Exact acknowledgement-loss retries do not create another observation.
	if err = db.SupersedeEvaluation(ctx, base.ID, revision); err != nil {
		t.Fatal(err)
	}
	replayed, err := db.ObservationSet(ctx, base.Key)
	if err != nil || !reflect.DeepEqual(replayed, set) {
		t.Fatal("exact replay changed observation set", replayed, err)
	}
	now := base.Time.Add(24 * time.Hour)
	aggregated, err := routing.AggregateEvidence(base.Key, replayed, now, routing.Defaults())
	if err != nil || aggregated.Samples != 1 || aggregated.Quality != 0 || !aggregated.WindowStart.Equal(base.Time) || !aggregated.WindowEnd.Equal(base.Time) {
		t.Fatal("correction was counted as another sample", aggregated, err)
	}
}

func TestObservationSetAuditSupersessionUsesSourceTimeAndStableID(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "audit-observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	newer := qualityTask(t, db, "audited", key.Model, "", "")
	newer.ID, newer.Time = "newer", time.Unix(300, 1).UTC()
	newer.Audit.Verdict, newer.Audit.Confidence = "reject", .7
	older := newer
	older.ID, older.Time = "older", time.Unix(200, 0).UTC()
	older.Audit.Verdict, older.Audit.Confidence = "accept", .9
	// Arrival order is intentionally opposite evidence-time order.
	if err = db.RecordAudit(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordAudit(ctx, older); err != nil {
		t.Fatal(err)
	}
	set, err := db.ObservationSet(ctx, key)
	if err != nil || len(set.Advisory) != 1 || set.Advisory[0].ID != newer.ID || set.Advisory[0].Quality != 0 || !set.Advisory[0].Time.Equal(newer.Time) {
		t.Fatal("arrival order selected advisory", set, err)
	}
	// Equal timestamps use immutable ID as the deterministic tie-breaker.
	tied := newer
	tied.ID, tied.Audit.Verdict = "z-tie", "accept"
	if err = db.RecordAudit(ctx, tied); err != nil {
		t.Fatal(err)
	}
	set, err = db.ObservationSet(ctx, key)
	if err != nil || len(set.Advisory) != 1 || set.Advisory[0].ID != tied.ID || set.Advisory[0].Quality != 1 {
		t.Fatal("stable identity did not break timestamp tie", set, err)
	}
	if err = db.RecordAudit(ctx, tied); err != nil {
		t.Fatal("exact audit replay failed", err)
	}
	replayed, err := db.ObservationSet(ctx, key)
	if err != nil || !reflect.DeepEqual(replayed, set) {
		t.Fatal("audit replay changed observation set", replayed, err)
	}
}

func TestObservationSetDirectEvidenceSuppressesAuditRegardlessOfArrival(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "precedence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	audit := qualityTask(t, db, "precedence", key.Model, "", "")
	audit.Audit.Verdict, audit.Audit.Confidence = "reject", 1
	audit.Time = time.Unix(500, 0).UTC()
	if err = db.RecordAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	direct := evaluation.Record{Version: 1, ID: "direct", TaskID: audit.TaskID, AttemptID: audit.AttemptID, Key: key, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "operator", Passed: true}}, ExecutionSucceeded: true, Time: time.Unix(100, 0).UTC()}
	if err = db.RecordEvaluation(ctx, direct); err != nil {
		t.Fatal(err)
	}
	set, err := db.ObservationSet(ctx, key)
	if err != nil || len(set.Fitness) != 1 || len(set.Advisory) != 0 || set.Fitness[0].ID != direct.ID {
		t.Fatal("newer audit overrode direct evidence", set, err)
	}
}

func TestObservationSetAcceptedLLMJudgeSuppressesSeparateAudit(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "judge-precedence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	audit := qualityTask(t, db, "judge-precedence", key.Model, "", "")
	audit.Audit.Verdict, audit.Audit.Confidence = "reject", 1
	if err = db.RecordAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	judge := evaluation.Record{Version: 1, ID: "judge", TaskID: audit.TaskID, AttemptID: audit.AttemptID, Key: key, Checks: []evaluation.Check{{Source: evaluation.LLMJudge, Reference: "accepted-judge", Passed: true}}, AllowJudge: true, ExecutionSucceeded: true, Time: time.Unix(100, 0).UTC()}
	if err = db.RecordEvaluation(ctx, judge); err != nil {
		t.Fatal(err)
	}
	set, err := db.ObservationSet(ctx, key)
	if err != nil || len(set.Fitness) != 1 || len(set.Advisory) != 0 || set.Fitness[0].ID != judge.ID || set.Fitness[0].Quality != 1 {
		t.Fatal("accepted judge and separate audit double-counted attempt", set, err)
	}
}

func TestObservationSetBindsAuditToDurableTaskDomain(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "audit-domain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	code := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	audit := qualityTask(t, db, "domain", code.Model, "", "")
	audit.Audit.Domain = "math"
	audit.Audit.Verdict, audit.Audit.Confidence = "accept", 1
	if err = db.RecordAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	mathKey := code
	mathKey.Domain = "math"
	for _, key := range []routing.Key{code, mathKey} {
		set, err := db.ObservationSet(ctx, key)
		if err != nil || len(set.Advisory) != 0 {
			t.Fatal("miscorrelated audit contaminated domain", key, set, err)
		}
	}
}

func TestObservationSetCanExcludeAuditStorageEntirely(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "excluded-audits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	audit := qualityTask(t, db, "excluded", key.Model, "", "")
	audit.Audit.Verdict, audit.Audit.Confidence = "accept", 1
	if err = db.RecordAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`UPDATE audit_records SET body='{' WHERE id=?`, audit.ID); err != nil {
		t.Fatal(err)
	}
	set, err := db.ObservationSet(ctx, key, false)
	if err != nil || len(set.Fitness) != 0 || len(set.Advisory) != 0 {
		t.Fatal("disabled advisory inspected corrupt audit storage", set, err)
	}
	if set, err = db.ObservationSet(ctx, key); err == nil || !reflect.DeepEqual(set, routing.ObservationSet{}) {
		t.Fatal("enabled advisory accepted corrupt audit storage", set, err)
	}
	if set, err = db.ObservationSet(ctx, key, true, false); !errors.Is(err, routing.ErrInvalid) || !reflect.DeepEqual(set, routing.ObservationSet{}) {
		t.Fatal("ambiguous advisory option accepted", set, err)
	}
}

func TestObservationSetFailsClosedOnCorrectionTopologyAndClockSkew(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "invalid-observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := revisionBase(t, db)
	if _, err = db.db.Exec(`DELETE FROM evaluation_heads WHERE base_id=?`, base.ID); err != nil {
		t.Fatal(err)
	}
	if set, err := db.ObservationSet(ctx, base.Key); err == nil || !reflect.DeepEqual(set, routing.ObservationSet{}) {
		t.Fatal("headless observation accepted", set, err)
	}
	if _, err = db.db.Exec(`INSERT INTO evaluation_heads(base_id,current_id) VALUES(?,?)`, base.ID, base.ID); err != nil {
		t.Fatal(err)
	}
	set, err := db.ObservationSet(ctx, base.Key)
	if err != nil {
		t.Fatal(err)
	}
	now := base.Time.Add(-time.Nanosecond)
	if _, err = routing.AggregateEvidence(base.Key, set, now, routing.Defaults()); !errors.Is(err, routing.ErrInvalid) {
		t.Fatal("future observation was clamped", base.Time, err)
	}
}

func TestObservationSetRejectsInvalidKeysAndCanceledRead(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "invalid-key.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	for _, invalid := range []routing.Key{{}, {Model: " candidate", Provider: key.Provider, Domain: key.Domain, Profile: key.Profile}, {Model: key.Model, Provider: key.Provider, Domain: "bad\ndomain", Profile: key.Profile}} {
		if set, err := db.ObservationSet(context.Background(), invalid); !errors.Is(err, routing.ErrInvalid) || !reflect.DeepEqual(set, routing.ObservationSet{}) {
			t.Fatal("invalid key accepted", invalid, set, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if set, err := db.ObservationSet(ctx, key); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(set, routing.ObservationSet{}) {
		t.Fatal("canceled read returned partial evidence", set, err)
	}
}

func TestObservationSetLargeHistoryCompletesWithinBoundedDeadline(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "large-observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6000; i++ {
		id := "evaluation-" + time.Unix(int64(i), 0).UTC().Format("150405.000000000")
		task, attempt := "task-"+id, "attempt-"+id
		recordKey := key
		if i >= 1000 {
			recordKey.Model = "irrelevant"
		}
		record := evaluation.Record{Version: 1, ID: id, TaskID: task, AttemptID: attempt, Key: recordKey, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: id, Passed: i%2 == 0}}, ExecutionSucceeded: true, Time: time.Unix(int64(i+1), 0).UTC()}
		body, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err = tx.Exec(`INSERT INTO task_heads VALUES(?,?,0,'completed')`, task, task); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO evaluations VALUES(?,?,?,?,?,?,?,?)`, id, task, attempt, recordKey.Model, recordKey.Provider, recordKey.Domain, recordKey.Profile, body); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO evaluation_heads VALUES(?,?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	set, err := db.ObservationSet(deadline, key)
	if err != nil || len(set.Fitness) != 1000 || len(set.Advisory) != 0 {
		t.Fatal("bounded set scan failed", len(set.Fitness), err)
	}
	for i := 1; i < len(set.Fitness); i++ {
		if set.Fitness[i-1].BaseID >= set.Fitness[i].BaseID {
			t.Fatal("observation ordering is not stable")
		}
	}
}

func TestObservationSetRejectsOverlongCorrectionChain(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "overlong-corrections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := revisionBase(t, db)
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	current := base
	for i := 0; i < 101; i++ {
		next := revised(current, fmt.Sprintf("revision-%03d", i), i%2 == 0)
		body, marshalErr := json.Marshal(next)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err = tx.Exec(`INSERT INTO evaluation_revisions(id,base_id,supersedes,body) VALUES(?,?,?,?)`, next.ID, base.ID, current.ID, body); err != nil {
			t.Fatal(err)
		}
		current = next
	}
	if _, err = tx.Exec(`UPDATE evaluation_heads SET current_id=? WHERE base_id=?`, current.ID, base.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if set, err := db.ObservationSet(ctx, base.Key); err == nil || !reflect.DeepEqual(set, routing.ObservationSet{}) {
		t.Fatal("overlong correction chain accepted", len(set.Fitness), err)
	}
}

func TestObservationSetRejectsAuditOverflowBeforeSuppression(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "audit-overflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "code", Profile: "default"}
	audit := qualityTask(t, db, "overflow", key.Model, "", "")
	direct := evaluation.Record{Version: 1, ID: "direct", TaskID: audit.TaskID, AttemptID: audit.AttemptID, Key: key, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "operator", Passed: true}}, ExecutionSucceeded: true, Time: time.Unix(100, 0).UTC()}
	if err = db.RecordEvaluation(ctx, direct); err != nil {
		t.Fatal(err)
	}
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxRoutingObservations; i++ {
		record := audit
		record.ID = fmt.Sprintf("audit-%05d", i)
		body, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err = tx.Exec(`INSERT INTO audit_records(id,task_id,body) VALUES(?,?,?)`, record.ID, record.TaskID, body); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if set, err := db.ObservationSet(ctx, key); err == nil || !reflect.DeepEqual(set, routing.ObservationSet{}) {
		t.Fatal("suppressed rows bypassed audit bound", len(set.Advisory), err)
	}
}
