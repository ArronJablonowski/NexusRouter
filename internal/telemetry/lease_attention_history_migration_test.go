package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestLeaseAttentionHistoryMigrationPreservesBaseline(t *testing.T) {
	s, _, req := approvalFixture(t)
	insertObservedLease(t, s, "baseline-token", req.TaskID, "private-scope", 1, req.CreatedAt.UnixNano())
	record := workers.LeaseAttention{Version: 1, ID: "baseline-attention", TaskID: req.TaskID, Writer: true, State: "open", Reason: "expired_unreleased", FirstObserved: req.CreatedAt, UpdatedAt: req.CreatedAt, LeaseExpires: req.CreatedAt}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; INSERT INTO lease_attention(id,lease_token,task_id,state,body) VALUES(?,?,?,'open',?); DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; PRAGMA user_version=24`, record.ID, "baseline-token", req.TaskID, body); err != nil {
		t.Fatal(err)
	}
	if err := s.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got []byte
	var sequence int64
	var kind string
	if err := s.db.QueryRow(`SELECT sequence,kind,body FROM lease_attention_history WHERE attention_id=?`, record.ID).Scan(&sequence, &kind, &got); err != nil || sequence != 1 || kind != "baseline" || !bytes.Equal(got, body) {
		t.Fatal("baseline changed or invented", sequence, kind, err)
	}
	var current []byte
	if err := s.db.QueryRow(`SELECT body FROM lease_attention WHERE id=?`, record.ID).Scan(&current); err != nil || !bytes.Equal(current, body) {
		t.Fatal("current observation changed", err)
	}
	if err := s.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count, version int
	if s.db.QueryRow(`SELECT count(*) FROM lease_attention_history`).Scan(&count) != nil || count != 1 || s.db.QueryRow(`PRAGMA user_version`).Scan(&version) != nil || version != 33 {
		t.Fatal("duplicate migration", count, version)
	}
	for _, statement := range []string{`INSERT INTO lease_attention_history VALUES('baseline-attention',0,'observed','{}')`, `INSERT INTO lease_attention_history VALUES('baseline-attention',2,'unknown','{}')`, `INSERT INTO lease_attention_history VALUES('missing',1,'baseline','{}')`, `INSERT INTO lease_attention_history VALUES('baseline-attention',1,'observed','{}')`} {
		if _, err := s.db.Exec(statement); err == nil {
			t.Fatal("constraint not enforced")
		}
	}
}
