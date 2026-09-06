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

// Called only after the actual worker process was killed and the parent journal
// was repaired. This exercises StartDispatcher's production sweep, not just the
// storage primitive, and retains full before/after source journal comparison.
func qualifyTerminalReaderSweep(t *testing.T, ctx context.Context, svc *Service, db *telemetry.Store, raw *sql.DB, lease telemetry.Lease, journals map[string][]runtime.Event) {
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
		leases, err := db.InspectLeases(wait, "delegation")
		if err != nil {
			t.Fatal(err)
		}
		if len(leases) == 0 {
			break
		}
		if len(leases) != 1 || leases[0].Token != lease.Token {
			t.Fatal("unexpected lease change during automatic sweep")
		}
		select {
		case <-tick.C:
		case <-wait.Done():
			t.Fatal("automatic terminal reader sweep did not reclaim the orphan")
		case <-dispatcher.done:
			t.Fatal("dispatcher exited before reader recovery")
		}
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatal("reader sweep degraded dispatcher", err)
	}
	var receipt []byte
	if err := raw.QueryRowContext(ctx, `SELECT body FROM lease_recoveries WHERE lease_token=?`, lease.Token).Scan(&receipt); err != nil {
		t.Fatal("missing atomic lease recovery receipt", err)
	}
	var record struct {
		Version  int    `json:"version"`
		Task     string `json:"task"`
		Sequence int64  `json:"sequence"`
		State    string `json:"state"`
		Reason   string `json:"reason"`
		Digest   string `json:"digest"`
	}
	hash := sha256.Sum256([]byte(lease.Token))
	if len(receipt) > 2048 || json.Unmarshal(receipt, &record) != nil || record.Version != 1 || record.Task != lease.TaskID || record.Sequence != int64(len(journals[lease.TaskID])) || record.State != "failed" || record.Reason != "terminal_reader_owner_unlocked" || record.Digest != hex.EncodeToString(hash[:]) {
		t.Fatal("invalid terminal reader receipt")
	}
	if strings.Contains(string(receipt), lease.Token) || strings.Contains(string(receipt), "durable worker candidate") || strings.Contains(string(receipt), "darwin-owner-") {
		t.Fatal("receipt exposed lease capability, output or guard path")
	}
	for range 2 {
		if _, count, err := db.RecoverTerminalReadersPage(ctx, "", 32, time.Now().UTC()); err != nil || count != 0 {
			t.Fatal("repeat sweep changed recovery", err, count)
		}
		if changed, err := db.RecoverTerminalReader(ctx, lease.Token, time.Now().UTC()); err != nil || changed {
			t.Fatal("repeat targeted recovery changed state", err)
		}
	}
	var after []byte
	var receipts int
	if err := raw.QueryRowContext(ctx, `SELECT body FROM lease_recoveries WHERE lease_token=?`, lease.Token).Scan(&after); err != nil || string(after) != string(receipt) {
		t.Fatal("repeat sweep rewrote audit receipt", err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM lease_recoveries`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal("duplicate or unrelated recovery receipt", err)
	}
	for task, before := range journals {
		page, err := db.ReadEventPage(ctx, task, 0, 100)
		if err != nil || page.HasMore || !reflect.DeepEqual(before, page.Events) {
			t.Fatal("lease sweep rewrote source journal", task, err)
		}
	}
	// Use a new fixture task, not a terminal task, to test resource availability.
	start := runtime.Event{Version: 1, ID: "lease-probe-start", TaskID: "lease-probe", SessionID: "lease-probe-session", CorrelationID: "lease-probe", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	if err := db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	writer, err := db.AcquireLease(ctx, start.TaskID, "lease-probe-writer", "delegation", true, time.Now(), time.Second)
	if err != nil {
		t.Fatal("recovered reader still blocks a new writer", err)
	}
	if err := db.ReleaseLease(ctx, writer.Token, writer.Owner); err != nil {
		t.Fatal(err)
	}
}
