package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestLeaseAttentionHistoryCLIFlags(t *testing.T) {
	path, id, options, ok := leaseAttentionHistoryFlags([]string{"--db", "path", "--id", "record"})
	if !ok || path != "path" || id != "record" || options.Limit != 25 || options.AfterSequence != 0 {
		t.Fatal("wrong defaults")
	}
	for _, extra := range [][]string{{"--id", "other"}, {"--limit", "0"}, {"--limit", "101"}, {"--limit", "+1"}, {"--limit", "01"}, {"--after-sequence", "-1"}, {"--after-sequence", "01"}, {"--after-sequence", "9223372036854775807"}, {"--after-sequence="}, {"--after-sequence", "0", "--after-sequence", "1"}, {"--private", "value"}, {"private-tail"}} {
		var out, errout bytes.Buffer
		args := append([]string{"resources", "attention-history", "--db", "private-path", "--id", "record"}, extra...)
		if Run(args, &out, &errout, "dev") != 2 || out.Len() != 0 || strings.Contains(errout.String(), "private") {
			t.Fatal("invalid arguments accepted or echoed")
		}
	}
}

func TestLeaseAttentionHistoryCLIStoredTransitions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "task", CorrelationID: "task", Sequence: 1, Time: now, Kind: runtime.TaskStarted}
	if err := store.Append(ctx, 0, e); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES('private-token','task','private-owner','private-scope',0,?,0)`, now.Add(-time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ObserveLeaseAttentionPage(ctx, "", now, 100); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM lease_attention`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARWIN_PROCESS_OWNER_DIR", "")
	var out, errout bytes.Buffer
	if code := Run([]string{"resources", "attention-history", "--db", path, "--id", id, "--after-sequence=0", "--limit=1"}, &out, &errout, "dev"); code != 0 {
		t.Fatal(code, errout.String())
	}
	var page workers.LeaseAttentionHistoryPage
	if json.Unmarshal(out.Bytes(), &page) != nil || page.Version != 1 || page.StorageSchema != stateschema.Current || !page.Available || page.AttentionID != id || len(page.Items) != 1 || page.Items[0].Sequence != 1 || page.Items[0].Kind != "observed" || page.Items[0].Observation.State != "open" {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "private-") || strings.Contains(out.String(), path) || errout.Len() != 0 {
		t.Fatal("private input exposed")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("history command mutated database", err)
	}
}

func TestLeaseAttentionHistoryCLILegacyReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	store, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy readers report unavailability, not an invented empty history.
	if _, err := db.Exec(`DROP INDEX evaluations_routing_key; DROP TABLE lease_attention_history; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=24`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARWIN_PROCESS_OWNER_DIR", "")
	t.Setenv("DARWIN_MODE", "invalid-private-config")
	args := []string{"resources", "attention-history", "--db", path, "--id", "record"}
	var out, errout bytes.Buffer
	if code := Run(args, &out, &errout, "dev"); code != 0 {
		t.Fatal(code, errout.String())
	}
	var page workers.LeaseAttentionHistoryPage
	if json.Unmarshal(out.Bytes(), &page) != nil || page.Version != 1 || page.StorageSchema != 24 || page.Available || len(page.Items) != 0 || page.AttentionID != "record" {
		t.Fatal(out.String())
	}
	if Run(args, brokenWriter{}, &errout, "dev") != 1 {
		t.Fatal("ignored writer error")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	out.Reset()
	if runLeaseAttentionHistoryContext(canceled, args[2:], &out, &errout) != 1 || out.Len() != 0 {
		t.Fatal("canceled inspection output")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy mutated", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	out.Reset()
	errout.Reset()
	if Run([]string{"resources", "attention-history", "--db", missing, "--id", "record"}, &out, &errout, "dev") != 1 || out.Len() != 0 || strings.Contains(errout.String(), missing) {
		t.Fatal("missing DB accepted or echoed")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("created DB", err)
	}
}
