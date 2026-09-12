package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type steeringUnreadable struct{ t *testing.T }

func (r steeringUnreadable) Read([]byte) (int, error) {
	r.t.Error("read before parsing")
	return 0, io.EOF
}

type steeringFailedWriter struct{}

func (steeringFailedWriter) Write([]byte) (int, error) { return 0, errors.New("private writer error") }

func TestSteerArgumentsBeforeInput(t *testing.T) {
	for _, args := range [][]string{nil, {"--config", "private"}, {"--config", "private", "--task", "task", "--key", "opaque", "--key", "again"}, {"--config=private", "--task=task", "--key=opaque", "extra"}, {"--config=private", "--task=task", "--key=opaque", "--unknown=value"}, {"--config=private", "--task=task", "--key="}} {
		var out, errout bytes.Buffer
		if code := runSteerContext(context.Background(), args, steeringUnreadable{t}, &out, &errout); code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "private") {
			t.Fatal(code, out.String(), errout.String())
		}
	}
	for _, args := range [][]string{{"--config", "private", "--task", "task", "--key", "opaque"}, {"--key=opaque", "--task=task", "--config=private"}} {
		if _, err := parseSteeringFlags(args, "config", "task", "key"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSteerInvalidInputAndConfiguration(t *testing.T) {
	for _, input := range []string{"", " ", string([]byte{255}), strings.Repeat("x", runtime.MaxSteeringBytes+1), "valid guidance"} {
		var out, errout bytes.Buffer
		code := runSteerContext(context.Background(), []string{"--config", "/missing/private-config", "--task", "task", "--key", "private-key"}, strings.NewReader(input), &out, &errout)
		if code != 1 || out.Len() != 0 || strings.Contains(errout.String(), "private") {
			t.Fatal(code, out.String(), errout.String())
		}
	}
}

func TestSteeringReceiptListAndSensitiveShow(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(dir, "tasks.db")
	configPath := filepath.Join(dir, "settings.yaml")
	body := []byte(fmt.Sprintf("telemetry:\n  database: %q\n", cfg.Telemetry.Database))
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "task", CorrelationID: "task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted}
	if err = db.Append(context.Background(), 0, e); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", configPath, "--task", "task", "--key", "opaque"}
	var out, errout bytes.Buffer
	if code := runSteerContext(context.Background(), args, strings.NewReader("sensitive guidance"), &out, &errout); code != 0 {
		t.Fatal(code, errout.String())
	}
	var receipt steeringReceipt
	if json.Unmarshal(out.Bytes(), &receipt) != nil || receipt.ID == "" || receipt.State != "pending" || strings.Contains(out.String(), "sensitive guidance") {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := runSteering([]string{"list", "--db", cfg.Telemetry.Database, "--task", "task"}, &out, &errout); code != 0 || strings.Contains(out.String(), "sensitive guidance") || !strings.Contains(out.String(), receipt.ID) {
		t.Fatal(code, out.String(), errout.String())
	}
	out.Reset()
	if code := runSteering([]string{"show", "--db", cfg.Telemetry.Database, "--task", "task", "--id", receipt.ID}, &out, &errout); code != 0 || !strings.Contains(out.String(), "sensitive guidance") {
		t.Fatal(code, out.String(), errout.String())
	}
	if code := runSteering([]string{"show", "--db", cfg.Telemetry.Database, "--task", "task", "--id", receipt.ID}, steeringFailedWriter{}, &errout); code != 1 {
		t.Fatal(code)
	}
	if code := runSteerContext(context.Background(), args, strings.NewReader("sensitive guidance"), steeringFailedWriter{}, &errout); code != 1 {
		t.Fatal(code)
	}
	if strings.Contains(errout.String(), "private writer") {
		t.Fatal("error leaked")
	}
}

func TestSteeringReadOnlyMissingAndInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	var out, errout bytes.Buffer
	if code := runSteering([]string{"list", "--db", path, "--task", "task"}, &out, &errout); code != 1 {
		t.Fatal(code)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created DB", err)
	}
	for _, args := range [][]string{{"show", "--db", path, "--task", "task"}, {"list", "--db", path, "--task", "task", "--id", "id"}, {"list", "--db", path, "--task", "task", "--task", "other"}, {"delete", "--db", path, "--task", "task"}} {
		if code := runSteering(args, &out, &errout); code != 2 {
			t.Fatal(code)
		}
	}
}

func TestSteeringLegacyReadOnlyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "task", CorrelationID: "task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted}
	if err = db.Append(context.Background(), 0, e); err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`DROP INDEX evaluations_routing_key; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=13`); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := runSteering([]string{"list", "--db", path, "--task", "task"}, &out, &errout); code != 0 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatal(code, out.String(), errout.String())
	}
	var version int
	if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 13 {
		t.Fatal("schema changed", version, err)
	}
}
