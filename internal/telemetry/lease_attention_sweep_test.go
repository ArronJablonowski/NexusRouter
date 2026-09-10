package telemetry

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workers"
	"modernc.org/sqlite"
)

func attentionSweepFixture(t *testing.T, count int) (*Store, string, time.Time) {
	t.Helper()
	s, _, req := approvalFixture(t)
	now := req.CreatedAt.Add(time.Second)
	for i := 1; i <= count; i++ {
		insertObservedLease(t, s, fmt.Sprint("sweep-", i), req.TaskID, fmt.Sprint("scope-", i), 1, now.UnixNano())
	}
	return s, req.TaskID, now
}

func TestLeaseAttentionSweepIsolatesCorruptCandidates(t *testing.T) {
	for _, badIndex := range []int{1, 2} {
		t.Run(strconv.Itoa(badIndex), func(t *testing.T) {
			s, task, now := attentionSweepFixture(t, 3)
			ctx := context.Background()
			if _, err := s.db.Exec(`PRAGMA ignore_check_constraints=ON; UPDATE resource_leases SET writer=2 WHERE token=?`, fmt.Sprint("sweep-", badIndex)); err != nil {
				t.Fatal(err)
			}
			before, err := s.Read(ctx, task, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			cursor, changed, err := s.SweepLeaseAttentionPage(ctx, "", now, 3)
			if cursor != "3" || changed != 2 || !errors.Is(err, workers.ErrLeaseAttention) {
				t.Fatal(cursor, changed, err)
			}
			var badRecords, released int
			if s.db.QueryRow(`SELECT count(*) FROM lease_attention WHERE lease_token=?`, fmt.Sprint("sweep-", badIndex)).Scan(&badRecords) != nil || badRecords != 0 {
				t.Fatal("bad row produced projection")
			}
			if s.db.QueryRow(`SELECT sum(released) FROM resource_leases`).Scan(&released) != nil || released != 0 {
				t.Fatal("sweep released authority")
			}
			for i := 1; i <= 3; i++ {
				if i == badIndex {
					continue
				}
				a, _ := attentionRecord(t, s, fmt.Sprint("sweep-", i))
				history, err := s.ListLeaseAttentionHistory(ctx, a.ID, workers.LeaseAttentionHistoryOptions{Limit: 100})
				if err != nil || len(history.Items) != 1 || history.Items[0].Sequence != 1 {
					t.Fatal(history, err)
				}
			}
			after, err := s.Read(ctx, task, 0, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("journal changed", err)
			}
			var writer int
			if s.db.QueryRow(`SELECT writer FROM resource_leases WHERE token=?`, fmt.Sprint("sweep-", badIndex)).Scan(&writer) != nil || writer != 2 {
				t.Fatal("bad authority repaired")
			}
			cursor, changed, err = s.SweepLeaseAttentionPage(ctx, cursor, now, 3)
			if cursor != "" || changed != 0 || err != nil {
				t.Fatal("end wrap", cursor, changed, err)
			}
		})
	}
}

func TestLeaseAttentionSweepRollbackDoesNotStarveLater(t *testing.T) {
	for _, mode := range []string{"ABORT", "IGNORE", "corrupt-head"} {
		t.Run(mode, func(t *testing.T) {
			s, _, now := attentionSweepFixture(t, 3)
			ctx := context.Background()
			var original []byte
			if mode == "corrupt-head" {
				if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", now, 3); err != nil || n != 3 {
					t.Fatal(n, err)
				}
				_, original = attentionRecord(t, s, "sweep-2")
				if _, err := s.db.Exec(`UPDATE lease_attention_history SET body='{}' WHERE attention_id=(SELECT id FROM lease_attention WHERE lease_token='sweep-2'); UPDATE resource_leases SET released=1`); err != nil {
					t.Fatal(err)
				}
			} else {
				raise := "RAISE(IGNORE)"
				if mode == "ABORT" {
					raise = "RAISE(ABORT,'fixture')"
				}
				if _, err := s.db.Exec(`CREATE TRIGGER reject_sweep BEFORE INSERT ON lease_attention_history WHEN NEW.attention_id=(SELECT id FROM lease_attention WHERE lease_token='sweep-2') BEGIN SELECT ` + raise + `; END`); err != nil {
					t.Fatal(err)
				}
			}
			cursor, n, err := s.SweepLeaseAttentionPage(ctx, "", now.Add(time.Second), 3)
			if cursor != "3" || n != 2 || !errors.Is(err, workers.ErrLeaseAttention) {
				t.Fatal(cursor, n, err)
			}
			if mode == "corrupt-head" {
				_, body := attentionRecord(t, s, "sweep-2")
				if !bytes.Equal(original, body) {
					t.Fatal("bad projection changed")
				}
			} else {
				var count int
				if s.db.QueryRow(`SELECT count(*) FROM lease_attention WHERE lease_token='sweep-2'`).Scan(&count) != nil || count != 0 {
					t.Fatal("failed history partially committed projection")
				}
			}
			for _, token := range []string{"sweep-1", "sweep-3"} {
				a, _ := attentionRecord(t, s, token)
				history, err := s.ListLeaseAttentionHistory(ctx, a.ID, workers.LeaseAttentionHistoryOptions{Limit: 100})
				want := 1
				if mode == "corrupt-head" {
					want = 2
				}
				if err != nil || len(history.Items) != want {
					t.Fatal(history, err)
				}
			}
		})
	}
}

func TestLeaseAttentionSweepBoundsAndQueryFailure(t *testing.T) {
	s, _, now := attentionSweepFixture(t, 3)
	ctx := context.Background()
	for _, cursor := range []string{"0", "01", "-1", "+1", "token", "9223372036854775808"} {
		next, n, err := s.SweepLeaseAttentionPage(ctx, cursor, now, 1)
		if next != cursor || n != 0 || err == nil {
			t.Fatal(next, n, err)
		}
	}
	for _, limit := range []int{0, 101} {
		if next, n, err := s.SweepLeaseAttentionPage(ctx, "", now, limit); next != "" || n != 0 || err == nil {
			t.Fatal(next, n, err)
		}
	}
	for _, at := range []time.Time{{}, time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, n, err := s.SweepLeaseAttentionPage(ctx, "", at, 1); n != 0 || err == nil {
			t.Fatal(n, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, c := range []context.Context{nil, canceled} {
		if next, n, err := s.SweepLeaseAttentionPage(c, "1", now, 1); next != "1" || n != 0 || err == nil {
			t.Fatal(next, n, err)
		}
	}
	cursor, n, err := s.SweepLeaseAttentionPage(ctx, "", now, 1)
	if cursor != "1" || n != 1 || err != nil {
		t.Fatal(cursor, n, err)
	}
	cursor, n, err = s.SweepLeaseAttentionPage(ctx, cursor, now, 3)
	if cursor != "" || n != 2 || err != nil {
		t.Fatal(cursor, n, err)
	}
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=24`); err != nil {
		t.Fatal(err)
	}
	if next, n, err := s.SweepLeaseAttentionPage(ctx, "2", now, 1); next != "2" || n != 0 || err == nil {
		t.Fatal("unsupported schema advanced", next, n, err)
	}
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=26; ALTER TABLE resource_leases RENAME TO hidden_leases`); err != nil {
		t.Fatal(err)
	}
	if next, n, err := s.SweepLeaseAttentionPage(ctx, "2", now, 1); next != "2" || n != 0 || err == nil {
		t.Fatal("query failure advanced", next, n, err)
	}
}

var attentionSweepCancelFunction atomic.Uint64

func TestLeaseAttentionSweepSelectedRowDoesNotFallThrough(t *testing.T) {
	for _, mode := range []string{"deleted", "renewed"} {
		t.Run(mode, func(t *testing.T) {
			s, _, now := attentionSweepFixture(t, 3)
			ctx := context.Background()
			selected, err := s.attentionSweepCandidates(ctx, 0, now, 3)
			if err != nil || !reflect.DeepEqual(selected, []int64{1, 2, 3}) {
				t.Fatal(selected, err)
			}
			if mode == "deleted" {
				_, err = s.db.Exec(`DELETE FROM resource_leases WHERE token='sweep-2'`)
			} else {
				_, err = s.db.Exec(`UPDATE resource_leases SET expires=? WHERE token='sweep-2'`, now.Add(time.Hour).UnixNano())
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, n, err := s.observeLeaseAttentionPage(ctx, "1", now, 1, selected[1]); err != nil || n != 0 {
				t.Fatal("stale selected row observed", n, err)
			}
			var count int
			if s.db.QueryRow(`SELECT count(*) FROM lease_attention`).Scan(&count) != nil || count != 0 {
				t.Fatal("observation fell through to later row")
			}
			if _, n, err := s.observeLeaseAttentionPage(ctx, "2", now, 1, selected[2]); err != nil || n != 1 {
				t.Fatal("later row not independently available", n, err)
			}
		})
	}
}

func TestLeaseAttentionSweepCancellationPreservesUnattempted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	name := fmt.Sprintf("attention_sweep_cancel_%d", attentionSweepCancelFunction.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		cancel()
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	s, _, now := attentionSweepFixture(t, 3)
	if _, err := s.db.Exec(`CREATE TRIGGER cancel_attention BEFORE INSERT ON lease_attention WHEN NEW.lease_token='sweep-2' BEGIN SELECT ` + name + `(); END`); err != nil {
		t.Fatal(err)
	}
	next, n, err := s.SweepLeaseAttentionPage(ctx, "", now, 3)
	if next != "2" || n != 1 || err == nil {
		t.Fatal("wrong cancellation cursor", next, n, err)
	}
	var tokens []string
	rows, err := s.db.Query(`SELECT lease_token FROM lease_attention ORDER BY lease_token`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	rows.Close()
	if body, _ := json.Marshal(tokens); string(body) != `["sweep-1"]` {
		t.Fatal("canceled/unattempted persisted", tokens)
	}
	if _, err := s.db.Exec(`DROP TRIGGER cancel_attention`); err != nil {
		t.Fatal(err)
	}
	next, n, err = s.SweepLeaseAttentionPage(context.Background(), next, now, 3)
	if next != "" || n != 1 || err != nil {
		t.Fatal("later unattempted row skipped", next, n, err)
	}
	next, n, err = s.SweepLeaseAttentionPage(context.Background(), "", now, 3)
	if next != "3" || n != 1 || err != nil {
		t.Fatal("canceled row not revisited", next, n, err)
	}
}
