package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestLeaseAttentionHistoryConcurrentObservers(t *testing.T) {
	s, path, req := approvalFixture(t)
	ctx := context.Background()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	now := req.CreatedAt.Add(time.Second)
	insertObservedLease(t, s, "history-private-token", req.TaskID, "history-private-scope", 1, now.UnixNano())
	observe := func(at time.Time, sequence int) {
		t.Helper()
		barrier := make(chan struct{})
		type result struct {
			changed int
			err     error
		}
		results := make(chan result, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for _, store := range []*Store{s, other} {
			go func() {
				ready.Done()
				<-barrier
				_, changed, err := store.ObserveLeaseAttentionPage(ctx, "", at, 100)
				results <- result{changed, err}
			}()
		}
		ready.Wait()
		close(barrier)
		total := 0
		for range 2 {
			out := <-results
			if out.err != nil {
				t.Fatal("concurrent observer failed", out.err)
			}
			total += out.changed
		}
		if total != 1 {
			t.Fatal("concurrent observers duplicated transition", total)
		}
		var projections, history, head int
		if s.db.QueryRow(`SELECT count(*) FROM lease_attention`).Scan(&projections) != nil || projections != 1 {
			t.Fatal("projection count", projections)
		}
		if s.db.QueryRow(`SELECT count(*),max(sequence) FROM lease_attention_history`).Scan(&history, &head) != nil || history != sequence || head != sequence {
			t.Fatal("history count/head", history, head)
		}
	}
	observe(now, 1)
	first, _ := attentionRecord(t, s, "history-private-token")
	if _, err := s.db.Exec(`UPDATE resource_leases SET expires=? WHERE token='history-private-token'`, now.Add(time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	observe(now.Add(time.Second), 2)
	page, err := s.ListLeaseAttentionHistory(ctx, first.ID, workers.LeaseAttentionHistoryOptions{Limit: 100})
	if err != nil || len(page.Items) != 2 || page.Items[0].Sequence != 1 || page.Items[1].Sequence != 2 || page.Items[0].Observation.Reason != "expired_unreleased" || page.Items[1].Observation.Reason != "lease_renewed" {
		t.Fatal(page, err)
	}
}

func attentionHistoryFixture(t *testing.T) (*Store, string, workers.LeaseAttention, time.Time) {
	t.Helper()
	s, path, req := approvalFixture(t)
	now := req.CreatedAt.Add(time.Second)
	insertObservedLease(t, s, "history-private-token", req.TaskID, "history-private-scope", 1, now.UnixNano())
	if _, n, err := s.ObserveLeaseAttentionPage(context.Background(), "", now, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	a, _ := attentionRecord(t, s, "history-private-token")
	return s, path, a, now
}

func appendAttentionFixtureState(t *testing.T, s *Store, expiry, now time.Time, released bool) {
	t.Helper()
	flag := 0
	if released {
		flag = 1
	}
	if _, err := s.db.Exec(`UPDATE resource_leases SET expires=?,released=? WHERE token='history-private-token'`, expiry.UnixNano(), flag); err != nil {
		t.Fatal(err)
	}
	if _, n, err := s.ObserveLeaseAttentionPage(context.Background(), "", now, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestLeaseAttentionHistoryLifecycleAndReadOnly(t *testing.T) {
	s, path, a, now := attentionHistoryFixture(t)
	ctx := context.Background()
	expiry := now.Add(time.Hour)
	appendAttentionFixtureState(t, s, expiry, now.Add(time.Second), false)
	appendAttentionFixtureState(t, s, expiry, expiry, false)
	appendAttentionFixtureState(t, s, expiry, expiry.Add(time.Second), true)
	if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", expiry.Add(2*time.Second), 100); err != nil || n != 0 {
		t.Fatal("unchanged observation appended", n, err)
	}
	all, err := s.ListLeaseAttentionHistory(ctx, a.ID, workers.LeaseAttentionHistoryOptions{Limit: 100})
	if err != nil || all.Validate() != nil || len(all.Items) != 4 || all.HasMore {
		t.Fatal(all, err)
	}
	want := []string{"expired_unreleased", "lease_renewed", "expired_unreleased", "lease_released"}
	for i, item := range all.Items {
		if item.Sequence != int64(i+1) || item.Kind != "observed" || item.Observation.Reason != want[i] {
			t.Fatal(item)
		}
	}
	first, err := s.ListLeaseAttentionHistory(ctx, a.ID, workers.LeaseAttentionHistoryOptions{Limit: 2})
	if err != nil || !first.HasMore || first.NextSequence != 2 || !reflect.DeepEqual(first.Items, all.Items[:2]) {
		t.Fatal(first, err)
	}
	last, err := s.ListLeaseAttentionHistory(ctx, a.ID, workers.LeaseAttentionHistoryOptions{AfterSequence: 2, Limit: 2})
	if err != nil || last.HasMore || last.NextSequence != 0 || !reflect.DeepEqual(last.Items, all.Items[2:]) {
		t.Fatal(last, err)
	}
	for _, after := range []int64{4, 100} {
		page, err := s.ListLeaseAttentionHistory(ctx, a.ID, workers.LeaseAttentionHistoryOptions{AfterSequence: after, Limit: 2})
		if err != nil || len(page.Items) != 0 || page.HasMore {
			t.Fatal(page, err)
		}
	}
	body, _ := json.Marshal(all)
	for _, secret := range []string{"history-private-token", "history-private-scope", "private-owner", "process_id", "darwin-owner-"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("private authority leaked")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ListLeaseAttentionHistory(ctx, a.ID, workers.LeaseAttentionHistoryOptions{Limit: 100})
	if err != nil || !reflect.DeepEqual(got, all) {
		t.Fatal(got, err)
	}
	r.Close()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("readonly history changed database", err)
	}
}

func TestLeaseAttentionHistoryInputsAndLegacy(t *testing.T) {
	s, _, a, _ := attentionHistoryFixture(t)
	ctx := context.Background()
	if _, err := s.ListLeaseAttentionHistory(ctx, "missing", workers.LeaseAttentionHistoryOptions{Limit: 1}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing attention", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, c := range []context.Context{nil, canceled} {
		if _, err := s.ListLeaseAttentionHistory(c, a.ID, workers.LeaseAttentionHistoryOptions{Limit: 1}); err == nil {
			t.Fatal("invalid context")
		}
	}
	for _, o := range []workers.LeaseAttentionHistoryOptions{{Limit: 0}, {Limit: 101}, {Limit: 1, AfterSequence: -1}, {Limit: 1, AfterSequence: math.MaxInt64}} {
		if _, err := s.ListLeaseAttentionHistory(ctx, a.ID, o); err == nil {
			t.Fatal("invalid options")
		}
	}
	if _, err := s.ListLeaseAttentionHistory(ctx, "invalid\n", workers.LeaseAttentionHistoryOptions{Limit: 1}); err == nil {
		t.Fatal("invalid id")
	}
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=24`); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListLeaseAttentionHistory(ctx, a.ID, workers.LeaseAttentionHistoryOptions{Limit: 1})
	if err != nil || page.Validate() != nil || page.Available || page.StorageSchema != 24 || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='lease_attention_history'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy inspection migrated", err)
	}
}

func TestLeaseAttentionHistoryAtomicAppend(t *testing.T) {
	for _, kind := range []string{"ABORT", "IGNORE"} {
		for _, initial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-initial-%v", kind, initial), func(t *testing.T) {
				s, _, a, now := attentionHistoryFixture(t)
				_, before := attentionRecord(t, s, "history-private-token")
				if initial {
					if _, err := s.db.Exec(`DELETE FROM lease_attention_history; DELETE FROM lease_attention`); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := s.db.Exec(`UPDATE resource_leases SET released=1`); err != nil {
						t.Fatal(err)
					}
				}
				raise := "RAISE(IGNORE)"
				if kind == "ABORT" {
					raise = "RAISE(ABORT,'fixture reject')"
				}
				if _, err := s.db.Exec(`CREATE TRIGGER refuse_history BEFORE INSERT ON lease_attention_history BEGIN SELECT ` + raise + `; END`); err != nil {
					t.Fatal(err)
				}
				if cursor, n, err := s.ObserveLeaseAttentionPage(context.Background(), "", now.Add(time.Second), 100); err == nil || cursor != "" || n != 0 {
					t.Fatal("failed append acknowledged", cursor, n, err)
				}
				var count int
				want := 1
				if initial {
					want = 0
				}
				if s.db.QueryRow(`SELECT count(*) FROM lease_attention_history`).Scan(&count) != nil || count != want {
					t.Fatal("history partially committed", count)
				}
				if s.db.QueryRow(`SELECT count(*) FROM lease_attention`).Scan(&count) != nil || count != want {
					t.Fatal("projection partially committed", count)
				}
				if !initial {
					got, body := attentionRecord(t, s, "history-private-token")
					if got.ID != a.ID || !bytes.Equal(body, before) {
						t.Fatal("projection advanced without history")
					}
				}
			})
		}
	}
}

func TestLeaseAttentionHistoryRejectsCorruption(t *testing.T) {
	for _, mode := range []string{"head-mismatch", "gap", "kind", "canonical", "overflow", "missing-head", "duplicate-observation"} {
		t.Run(mode, func(t *testing.T) {
			s, _, a, now := attentionHistoryFixture(t)
			expiry := now.Add(time.Hour)
			appendAttentionFixtureState(t, s, expiry, now.Add(time.Second), false)
			appendAttentionFixtureState(t, s, expiry, expiry, false)
			_, before := attentionRecord(t, s, "history-private-token")
			var err error
			switch mode {
			case "head-mismatch":
				_, err = s.db.Exec(`UPDATE lease_attention_history SET body=(SELECT body FROM lease_attention_history WHERE sequence=1) WHERE sequence=3`)
			case "gap":
				_, err = s.db.Exec(`DELETE FROM lease_attention_history WHERE sequence=2`)
			case "kind":
				_, err = s.db.Exec(`PRAGMA ignore_check_constraints=ON; UPDATE lease_attention_history SET kind='unknown' WHERE sequence=2`)
			case "canonical":
				_, err = s.db.Exec(`UPDATE lease_attention_history SET body=CAST(body AS TEXT)||' ' WHERE sequence=2`)
			case "overflow":
				_, err = s.db.Exec(`UPDATE lease_attention_history SET sequence=9223372036854775807 WHERE sequence=3`)
			case "missing-head":
				_, err = s.db.Exec(`DELETE FROM lease_attention_history`)
			case "duplicate-observation":
				_, err = s.db.Exec(`UPDATE lease_attention_history SET body=(SELECT body FROM lease_attention_history WHERE sequence=1) WHERE sequence=2`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if page, err := s.ListLeaseAttentionHistory(context.Background(), a.ID, workers.LeaseAttentionHistoryOptions{Limit: 100}); err == nil || !reflect.DeepEqual(page, workers.LeaseAttentionHistoryPage{}) {
				t.Fatal("corrupt history accepted", page, err)
			}
			if mode == "head-mismatch" || mode == "missing-head" || mode == "overflow" {
				if _, err := s.db.Exec(`UPDATE resource_leases SET released=1`); err != nil {
					t.Fatal(err)
				}
				if _, n, err := s.ObserveLeaseAttentionPage(context.Background(), "", expiry.Add(time.Second), 100); err == nil || n != 0 {
					t.Fatal("invalid head advanced", n, err)
				}
				_, after := attentionRecord(t, s, "history-private-token")
				if !bytes.Equal(before, after) {
					t.Fatal("failed history check changed projection")
				}
			}
		})
	}
}
