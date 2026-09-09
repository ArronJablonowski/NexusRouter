package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func attentionLease(t *testing.T, s *Store, scope string, writer bool, expires time.Time) Lease {
	t.Helper()
	l, err := s.AcquireLease(context.Background(), "task", "private-attention-owner", scope, writer, expires.Add(-time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func attentionRecord(t *testing.T, s *Store, token string) (workers.LeaseAttention, []byte) {
	t.Helper()
	var body []byte
	if err := s.db.QueryRow(`SELECT body FROM lease_attention WHERE lease_token=?`, token).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var out workers.LeaseAttention
	if json.Unmarshal(body, &out) != nil {
		t.Fatal("bad attention body")
	}
	return out, body
}

func TestLeaseAttentionLifecycleDoesNotChangeAuthority(t *testing.T) {
	s, _ := leaseStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	expired := attentionLease(t, s, "private-attention-expired-scope", true, now.Add(-time.Minute))
	live := attentionLease(t, s, "private-attention-live-scope", false, now.Add(time.Hour))
	released := attentionLease(t, s, "private-attention-released-scope", false, now.Add(-time.Minute))
	if err := s.ReleaseLease(ctx, released.Token, released.Owner); err != nil {
		t.Fatal(err)
	}
	before, err := s.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", now, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	first, body := attentionRecord(t, s, expired.Token)
	if first.Version != 1 || first.ID == "" || first.TaskID != "task" || !first.Writer || first.State != "open" || first.Reason != "expired_unreleased" || !first.FirstObserved.Equal(now) || !first.UpdatedAt.Equal(now) || !first.LeaseExpires.Equal(expired.Expires) {
		t.Fatal(first)
	}
	for _, private := range []string{expired.Token, live.Token, released.Token, expired.Scope, expired.Owner, "darwin-owner-", "process_id"} {
		if bytes.Contains(body, []byte(private)) {
			t.Fatal("private authority leaked")
		}
	}
	if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", now.Add(time.Second), 100); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	_, same := attentionRecord(t, s, expired.Token)
	if !bytes.Equal(body, same) {
		t.Fatal("unchanged observation rewrote timestamp")
	}
	// Simulate renewed durable evidence independently of ownership: an expired
	// lease cannot be renewed through its normal execution API.
	newExpiry := now.Add(time.Hour)
	if _, err := s.db.Exec(`UPDATE resource_leases SET expires=? WHERE token=?`, newExpiry.UnixNano(), expired.Token); err != nil {
		t.Fatal(err)
	}
	if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", now.Add(2*time.Second), 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	renewed, _ := attentionRecord(t, s, expired.Token)
	if renewed.ID != first.ID || renewed.State != "resolved" || renewed.Reason != "lease_renewed" || !renewed.FirstObserved.Equal(first.FirstObserved) || !renewed.LeaseExpires.Equal(newExpiry) {
		t.Fatal(renewed)
	}
	if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", newExpiry.Add(time.Second), 100); err != nil || n != 2 {
		t.Fatal(n, err)
	} // the other live reader expires too
	reopened, _ := attentionRecord(t, s, expired.Token)
	if reopened.ID != first.ID || reopened.State != "open" || reopened.Reason != "expired_unreleased" || !reopened.FirstObserved.Equal(first.FirstObserved) {
		t.Fatal(reopened)
	}
	if err := s.ReleaseLease(ctx, expired.Token, expired.Owner); err != nil {
		t.Fatal(err)
	}
	if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", newExpiry.Add(2*time.Second), 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	closed, closedBody := attentionRecord(t, s, expired.Token)
	if closed.ID != first.ID || closed.State != "resolved" || closed.Reason != "lease_released" || !closed.FirstObserved.Equal(first.FirstObserved) {
		t.Fatal(closed)
	}
	if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", newExpiry.Add(3*time.Second), 100); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	_, repeat := attentionRecord(t, s, expired.Token)
	if !bytes.Equal(closedBody, repeat) {
		t.Fatal("resolved observation rewrote timestamp")
	}
	after, err := s.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("observer rewrote task", err)
	}
	var held int
	if err := s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, live.Token).Scan(&held); err != nil || held != 0 {
		t.Fatal("observer released expired reader", err)
	}
}

func TestLeaseAttentionObservationPaginationAndValidation(t *testing.T) {
	s, _ := leaseStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		attentionLease(t, s, "attention-page-"+strconv.Itoa(i), false, now.Add(-time.Minute))
	}
	cursor := ""
	for range 3 {
		next, n, err := s.ObserveLeaseAttentionPage(ctx, cursor, now, 1)
		if err != nil || n != 1 || next == "" || next == cursor {
			t.Fatal(next, n, err)
		}
		cursor = next
	}
	if next, n, err := s.ObserveLeaseAttentionPage(ctx, cursor, now, 1); err != nil || n != 0 || next != "" {
		t.Fatal(next, n, err)
	}
	for _, after := range []string{"0", "01", "-1", "+1", "token", "9223372036854775808"} {
		if _, n, err := s.ObserveLeaseAttentionPage(ctx, after, now, 1); err == nil || n != 0 {
			t.Fatal("invalid cursor admitted", after, n)
		}
	}
	for _, limit := range []int{0, 101, -1} {
		if _, _, err := s.ObserveLeaseAttentionPage(ctx, "", now, limit); err == nil {
			t.Fatal("invalid limit")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if next, n, err := s.ObserveLeaseAttentionPage(canceled, "2", now, 1); err == nil || next != "2" || n != 0 {
		t.Fatal("canceled cursor mutated", next, n, err)
	}
	for _, at := range []time.Time{{}, time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, _, err := s.ObserveLeaseAttentionPage(ctx, "", at, 1); err == nil {
			t.Fatal("invalid observation time")
		}
	}
}

func TestLeaseAttentionPageRollbackAndMalformedEvidence(t *testing.T) {
	for _, mode := range []string{"trigger", "bad_expiry", "bad_record"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := leaseStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			first := attentionLease(t, s, "attention-rollback-a", false, now.Add(-time.Minute))
			second := attentionLease(t, s, "attention-rollback-b", false, now.Add(-time.Minute))
			switch mode {
			case "trigger":
				if _, err := s.db.Exec(`CREATE TRIGGER reject_attention BEFORE INSERT ON lease_attention WHEN NEW.lease_token='` + second.Token + `' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
					t.Fatal(err)
				}
			case "bad_expiry":
				if _, err := s.db.Exec(`UPDATE resource_leases SET expires=-1 WHERE token=?`, second.Token); err != nil {
					t.Fatal(err)
				}
			case "bad_record":
				if _, _, err := s.ObserveLeaseAttentionPage(ctx, "", now, 100); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`UPDATE lease_attention SET body='{}' WHERE lease_token=?`, second.Token); err != nil {
					t.Fatal(err)
				}
				// Remove the first synthetic observation's dependent history too,
				// so the next sweep attempts a new insert before the corrupt row.
				if _, err := s.db.Exec(`DELETE FROM lease_attention_history WHERE attention_id IN (SELECT id FROM lease_attention WHERE lease_token=?)`, first.Token); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`DELETE FROM lease_attention WHERE lease_token=?`, first.Token); err != nil {
					t.Fatal(err)
				}
			}
			if next, n, err := s.ObserveLeaseAttentionPage(ctx, "", now, 100); err == nil || next != "" || n != 0 {
				t.Fatal("partial page committed", next, n, err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM lease_attention WHERE lease_token=?`, first.Token).Scan(&count); err != nil || count != 0 {
				t.Fatal("first observation escaped rollback", err, count)
			}
		})
	}
}

func TestLeaseAttentionReadOnlyListAndCorruption(t *testing.T) {
	s, path := leaseStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var tokens []string
	for i := 0; i < 3; i++ {
		tokens = append(tokens, attentionLease(t, s, "private-list-scope-"+strconv.Itoa(i), false, now.Add(-time.Minute)).Token)
	}
	if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", now, 100); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	first, err := ro.ListLeaseAttention(ctx, workers.LeaseAttentionOptions{State: "open", Limit: 2})
	if err != nil || first.Version != 1 || first.StorageSchema != currentStorageSchema || !first.Available || len(first.Items) != 2 || !first.HasMore {
		t.Fatal(first, err)
	}
	second, err := ro.ListLeaseAttention(ctx, workers.LeaseAttentionOptions{State: "all", After: first.Items[1].ID, Limit: 2})
	if err != nil || len(second.Items) != 1 || second.HasMore || second.Items[0].ID <= first.Items[1].ID || first.Items[0].ID >= first.Items[1].ID {
		t.Fatal(second, err)
	}
	body, _ := json.Marshal(first)
	for _, private := range append(tokens, "private-list-scope", "private-attention-owner", "lease_token", "process_id") {
		if strings.Contains(string(body), private) {
			t.Fatal("private metadata leaked")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("list modified database", err)
	}
	for _, opts := range []workers.LeaseAttentionOptions{{State: "unknown", Limit: 1}, {State: "all", Limit: 101}, {State: "all", Limit: 0}, {State: "all", After: "bad cursor", Limit: 1}} {
		if _, err := ro.ListLeaseAttention(ctx, opts); err == nil {
			t.Fatal("invalid list options")
		}
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec(`UPDATE lease_attention SET body=? WHERE lease_token=?`, strings.Repeat("x", 4097), tokens[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListLeaseAttention(ctx, workers.LeaseAttentionOptions{State: "all", Limit: 100}); err == nil {
		t.Fatal("oversized record published")
	}
}

func TestLeaseAttentionLegacyListDoesNotCreateSchema(t *testing.T) {
	s, path := leaseStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; PRAGMA user_version=23`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	page, err := ro.ListLeaseAttention(ctx, workers.LeaseAttentionOptions{State: "all", Limit: 10})
	if err != nil || page.Available || page.StorageSchema != 23 || page.Items == nil || len(page.Items) != 0 || page.HasMore {
		t.Fatal(page, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy inspection migrated DB", err)
	}
	missing := filepath.Join(t.TempDir(), "missing", "attention.db")
	if opened, err := OpenReadOnly(ctx, missing); err == nil {
		opened.Close()
		t.Fatal("created missing database")
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatal("created missing parent", err)
	}
}

func TestLeaseAttentionListRejectsNoncanonicalOrMisboundRecords(t *testing.T) {
	for _, mode := range []string{"unknown_field", "duplicate_field", "task_column", "state_column", "id_column", "timestamp"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := leaseStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			lease := attentionLease(t, s, "attention-corruption", false, now.Add(-time.Minute))
			if _, _, err := s.ObserveLeaseAttentionPage(ctx, "", now, 100); err != nil {
				t.Fatal(err)
			}
			record, body := attentionRecord(t, s, lease.Token)
			switch mode {
			case "unknown_field":
				body = append([]byte(`{"private":"hidden",`), body[1:]...)
			case "duplicate_field":
				body = append([]byte(`{"version":1,`), body[1:]...)
			case "task_column":
				record.TaskID = "another-task"
				body, _ = json.Marshal(record)
			case "state_column":
				record.State = "resolved"
				record.Reason = "lease_released"
				body, _ = json.Marshal(record)
			case "id_column":
				record.ID = "another-id"
				body, _ = json.Marshal(record)
			case "timestamp":
				record.FirstObserved = now.Add(time.Hour)
				body, _ = json.Marshal(record)
			}
			if _, err := s.db.Exec(`UPDATE lease_attention SET body=? WHERE lease_token=?`, body, lease.Token); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ListLeaseAttention(ctx, workers.LeaseAttentionOptions{State: "all", Limit: 100}); err == nil {
				t.Fatal("corrupt attention published")
			}
		})
	}
}

func TestLeaseAttentionUnknownOwnershipStillReportsWithoutProbe(t *testing.T) {
	for _, mode := range []string{"legacy", "corrupt_reference"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := leaseStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			lease := attentionLease(t, s, "unknown-attention", true, now.Add(-time.Second))
			if mode == "legacy" {
				if _, err := s.db.Exec(`UPDATE resource_leases SET process_id=NULL WHERE token=?`, lease.Token); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.db.Exec(`UPDATE lease_processes SET body='{}'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", now, 100); err != nil || n != 1 {
				t.Fatal("unknown holder hidden", n, err)
			}
			got, _ := attentionRecord(t, s, lease.Token)
			if got.State != "open" || got.Reason != "expired_unreleased" || !got.Writer {
				t.Fatal(got)
			}
			var released int
			if err := s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, lease.Token).Scan(&released); err != nil || released != 0 {
				t.Fatal("unknown holder released", err)
			}
		})
	}
}

func TestLeaseAttentionIgnoredMutationCannotReportSuccess(t *testing.T) {
	for _, mode := range []string{"insert", "update"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := leaseStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			lease := attentionLease(t, s, "ignored-attention", false, now.Add(-time.Minute))
			var before []byte
			if mode == "update" {
				if _, n, err := s.ObserveLeaseAttentionPage(ctx, "", now, 100); err != nil || n != 1 {
					t.Fatal(n, err)
				}
				_, before = attentionRecord(t, s, lease.Token)
				if err := s.ReleaseLease(ctx, lease.Token, lease.Owner); err != nil {
					t.Fatal(err)
				}
			}
			journal, err := s.Read(ctx, "task", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var expires, released int64
			if err := s.db.QueryRow(`SELECT expires,released FROM resource_leases WHERE token=?`, lease.Token).Scan(&expires, &released); err != nil {
				t.Fatal(err)
			}
			statement := `CREATE TRIGGER ignore_attention BEFORE INSERT ON lease_attention BEGIN SELECT RAISE(IGNORE); END`
			if mode == "update" {
				statement = `CREATE TRIGGER ignore_attention BEFORE UPDATE ON lease_attention BEGIN SELECT RAISE(IGNORE); END`
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if next, observed, err := s.ObserveLeaseAttentionPage(ctx, "", now.Add(time.Second), 100); err == nil || next != "" || observed != 0 {
				t.Fatal("ignored mutation reported success", next, observed, err)
			}
			if mode == "update" {
				_, after := attentionRecord(t, s, lease.Token)
				if !bytes.Equal(before, after) {
					t.Fatal("ignored update changed durable record")
				}
			} else {
				var count int
				if err := s.db.QueryRow(`SELECT count(*) FROM lease_attention`).Scan(&count); err != nil || count != 0 {
					t.Fatal("ignored insert created record", count, err)
				}
			}
			var afterExpires, afterReleased int64
			if err := s.db.QueryRow(`SELECT expires,released FROM resource_leases WHERE token=?`, lease.Token).Scan(&afterExpires, &afterReleased); err != nil || afterExpires != expires || afterReleased != released {
				t.Fatal("attention changed lease authority", err)
			}
			afterJournal, err := s.Read(ctx, "task", 0, 100)
			if err != nil || !reflect.DeepEqual(journal, afterJournal) {
				t.Fatal("attention changed journal", err)
			}
		})
	}
}
