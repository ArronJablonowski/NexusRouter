package telemetry

import (
	"context"
	"reflect"
	"testing"
)

func TestLeaseAttentionMigrationPreservesLeaseAndJournal(t *testing.T) {
	s, _, req := approvalFixture(t)
	insertObservedLease(t, s, "private-token", req.TaskID, "scope", 0, req.CreatedAt.UnixNano())
	before, err := readRecoveryLease(context.Background(), s.db, "private-token")
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.Read(context.Background(), req.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; INSERT INTO lease_recoveries(lease_token,digest,body) VALUES('private-token','preserved-digest','preserved-receipt'); DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; PRAGMA user_version=23`); err != nil {
		t.Fatal(err)
	}
	if err := s.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := readRecoveryLease(context.Background(), s.db, "private-token")
	if err != nil || after != before {
		t.Fatal("lease changed", err)
	}
	got, err := s.Read(context.Background(), req.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(got, history) {
		t.Fatal("journal changed", err)
	}
	var body, digest string
	if err := s.db.QueryRow(`SELECT digest,body FROM lease_recoveries WHERE lease_token='private-token'`).Scan(&digest, &body); err != nil || digest != "preserved-digest" || body != "preserved-receipt" {
		t.Fatal("prior receipt changed", err)
	}
	var version, count int
	if s.db.QueryRow(`PRAGMA user_version`).Scan(&version) != nil || version != currentStorageSchema || s.db.QueryRow(`SELECT count(*) FROM lease_attention`).Scan(&count) != nil || count != 0 {
		t.Fatal(version, count)
	}
	if _, err := s.db.Exec(`INSERT INTO lease_attention(id,lease_token,task_id,state,body) VALUES('attention','private-token',?,'open','{}')`, req.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE lease_attention SET state='ignored'`); err == nil {
		t.Fatal("invalid state accepted")
	}
	if _, err := s.db.Exec(`INSERT INTO lease_attention(id,lease_token,task_id,state,body) VALUES('duplicate','private-token',?,'open','{}')`, req.TaskID); err == nil {
		t.Fatal("duplicate lease accepted")
	}
	if _, err := s.db.Exec(`INSERT INTO lease_attention(id,lease_token,task_id,state,body) VALUES('missing','missing-token',?,'open','{}')`, req.TaskID); err == nil {
		t.Fatal("missing lease accepted")
	}
	if err := s.initialize(context.Background()); err != nil {
		t.Fatal("repeat initialization", err)
	}
	if s.db.QueryRow(`SELECT count(*) FROM lease_attention`).Scan(&count) != nil || count != 1 {
		t.Fatal("repeat lost attention")
	}
}
