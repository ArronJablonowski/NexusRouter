package usagestats_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/internal/usagestats"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestCloudOdometerPreservesMeasuredTurnsAndTripBoundaries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	s, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	appendTask := func(id string) {
		t.Helper()
		now := time.Now().UTC()
		seq := int64(0)
		appendEvent := func(kind runtime.Kind, turn string, data runtime.Data) {
			t.Helper()
			seq++
			e := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-%d", id, seq), TaskID: id, SessionID: id, CorrelationID: id, Sequence: seq, Time: now.Add(time.Duration(seq) * time.Second), Kind: kind, Data: data}
			if turn != "" {
				e.TurnID = turn
				e.AttemptID = turn + "-attempt"
			}
			if err := s.Append(ctx, seq-1, e); err != nil {
				t.Fatal(err)
			}
		}
		appendEvent(runtime.TaskStarted, "", runtime.Data{ProviderID: "cloud", ModelID: "model"})
		for i, u := range []*providers.Usage{nil, {InputTokens: 10, OutputTokens: 2}, {InputTokens: 15, OutputTokens: 3}} {
			turn := fmt.Sprintf("turn-%d", i)
			appendEvent(runtime.TurnStarted, turn, runtime.Data{ProviderID: "cloud", ModelID: "model"})
			appendEvent(runtime.TurnCompleted, turn, runtime.Data{Usage: u})
		}
		appendEvent(runtime.TaskCompleted, "", runtime.Data{})
	}
	kinds := map[[2]string]string{{"cloud", "model"}: "cloud"}
	appendTask("first")
	before, err := usagestats.Read(ctx, path, kinds, nil)
	if err != nil || before.Cloud.Lifetime.Input != "25" || before.Cloud.Lifetime.Output != "5" || before.Cloud.Lifetime.Unknown != 1 || before.Cloud.Lifetime.Partial != 1 || before.Cloud.Lifetime.Measured != 1 {
		t.Fatal(before, err)
	}
	reset, err := usagestats.Read(ctx, path, kinds, &usagestats.Reset{Version: 1, Locality: "cloud", Revision: 0, Confirm: true})
	if err != nil || reset.Cloud.Trip.Input != "0" || reset.Cloud.Trip.Unknown != 0 || reset.Cloud.Trip.Measured != 0 {
		t.Fatal(reset, err)
	}
	appendTask("second")
	after, err := usagestats.Read(ctx, path, kinds, nil)
	if err != nil || after.Cloud.Lifetime.Input != "50" || after.Cloud.Trip.Input != "25" || after.Cloud.Trip.Partial != 1 {
		t.Fatal(after, err)
	}
	// The display does not turn incomplete accounting records into complete usage.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var first accounting.Record
	rows, err := db.Query("SELECT body FROM usage_records")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var raw []byte
		var r accounting.Record
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &r) != nil || r.Usage != nil {
			t.Fatal("accounting record altered")
		}
		if r.TaskID == "first" {
			first = r
		}
	}
	rows.Close()
	next := accounting.CloneRecord(first)
	next.ID = "corrected-first"
	next.Usage = &providers.Usage{InputTokens: 40, OutputTokens: 8}
	correction := accounting.Correction{Version: 1, ID: next.ID, BaseID: first.ID, Supersedes: first.ID, Evidence: "fixture-provider-reconciliation", Reason: accounting.ProviderReconciliation, Record: next, RecordedAt: first.OccurredAt.Add(time.Second)}
	if err = s.CorrectUsage(ctx, correction); err != nil {
		t.Fatal(err)
	}
	corrected, err := usagestats.Read(ctx, path, kinds, nil)
	if err != nil || corrected.Cloud.Lifetime.Input != "65" || corrected.Cloud.Lifetime.Unknown != 1 || corrected.Cloud.Lifetime.Partial != 1 || corrected.Cloud.Trip.Input != "25" {
		t.Fatal("correction double-counted or leaked into trip", corrected, err)
	}
	// Corruption cannot create a fabricated measured subtotal.
	_, err = db.Exec(`UPDATE events SET body=json_set(body,'$.data.provider_id','other') WHERE task_id='second' AND sequence=4`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = usagestats.Read(ctx, path, kinds, nil); err == nil {
		t.Fatal("mismatched provider counted")
	}
}
