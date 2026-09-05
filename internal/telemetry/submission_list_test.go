package telemetry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func TestSubmissionListByteBoundNoSkips(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Nine maximum-size task lists exceed the page budget without large blobs.
	for n := 0; n < 9; n++ {
		id := fmt.Sprintf("job-%d", n)
		now := submissionTime(time.Now())
		if _, err = tx.Exec(`INSERT INTO submissions(id,key_digest,request_digest,config_digest,request,state,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?)`, id, submitDigest(id), submitDigest("{}"), submitDigest("config"), []byte("{}"), now, now); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 1000; j++ {
			task := fmt.Sprintf("%d-%04d-", n, j) + strings.Repeat("x", 120)
			if _, err = tx.Exec(`INSERT INTO task_heads VALUES(?,?,1,'running')`, task, "s"); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"kind":"task.started","data":{"submission_id":%q}}`, id)
			if _, err = tx.Exec(`INSERT INTO events VALUES(?,?,1,?)`, task, task, body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	p, err := db.ListSubmissions(ctx, submissions.ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if !p.HasMore || len(b) > submissions.MaxPageBytes || len(p.Items) == 0 {
		t.Fatalf("page size %d items %d", len(b), len(p.Items))
	}
	next, err := db.ListSubmissions(ctx, submissions.ListOptions{Limit: 100, After: p.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if next.HasMore || len(p.Items)+len(next.Items) != 9 || next.Items[0].ID != fmt.Sprintf("job-%d", len(p.Items)) {
		t.Fatalf("skipped rows: %d + %d", len(p.Items), len(next.Items))
	}
}

func TestSubmissionListLegacyReadOnly(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	if _, err := db.db.Exec(`DROP INDEX events_submission_start; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE submission_recoveries; DROP TABLE submissions; PRAGMA user_version=11`); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	p, err := ro.ListSubmissions(ctx, submissions.ListOptions{Limit: 100})
	if err != nil || p.Version != 1 || len(p.Items) != 0 || p.HasMore || p.NextCursor != "" {
		t.Fatalf("legacy %+v %v", p, err)
	}
	var version int
	if err = ro.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 11 {
		t.Fatalf("mutated schema: %d %v", version, err)
	}
}

func TestSubmissionListFenceFilterAndReadonly(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	a := queuedSubmission(t, db, "a")
	b := queuedSubmission(t, db, "b")
	c := queuedSubmission(t, db, "c")
	p, err := db.ListSubmissions(ctx, submissions.ListOptions{Limit: 1, State: "queued"})
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != a.ID || !p.HasMore {
		t.Fatalf("first: %+v %v", p, err)
	}
	queuedSubmission(t, db, "later")
	if _, err = db.CancelSubmission(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	next, err := db.ListSubmissions(ctx, submissions.ListOptions{Limit: 1, State: "queued", After: p.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != c.ID || next.HasMore {
		t.Fatalf("next: %+v %v", next, err)
	}
	if _, err = db.ListSubmissions(ctx, submissions.ListOptions{Limit: 1, State: "running", After: p.NextCursor}); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatalf("filter mismatch: %v", err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	p, err = ro.ListSubmissions(ctx, submissions.ListOptions{Limit: 100})
	if err != nil || len(p.Items) != 4 {
		t.Fatalf("readonly: %+v %v", p, err)
	}
}

func TestSubmissionListOpaqueFieldsAndCorruption(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	a := queuedSubmission(t, db, "a")
	// Invalid, oversized opaque columns must never be loaded or decoded by discovery.
	if _, err := db.db.Exec(`UPDATE submissions SET request=zeroblob(9000000),result=zeroblob(9000000),token=? WHERE id=?`, strings.Repeat("secret", 100000), a.ID); err != nil {
		t.Fatal(err)
	}
	p, err := db.ListSubmissions(ctx, submissions.ListOptions{Limit: 100})
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("opaque payload read: %v", err)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "result") || strings.Contains(string(b), "secret") || strings.Contains(string(b), "token") {
		t.Fatalf("opaque leak")
	}
	if _, err = db.db.Exec(`UPDATE submissions SET error_code=? WHERE id=?`, strings.Repeat("private", 100000), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ListSubmissions(ctx, submissions.ListOptions{Limit: 100}); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatalf("corrupt metadata: %v", err)
	}
}

func TestSubmissionListCursorValidationAndLease(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "a")
	if _, err := db.ClaimSubmission(ctx, submitDigest("config"), time.Now(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	p, err := db.ListSubmissions(ctx, submissions.ListOptions{Limit: 1})
	if err != nil || !p.Items[0].LeaseExpired {
		t.Fatalf("lease: %+v %v", p, err)
	}
	for _, raw := range []string{`{"version":1,"last":0,"high_water":1,"state":"","extra":1}`, `{"version":1,"last":0,"last":0,"high_water":1,"state":""}`, `{"version":1,"last":2,"high_water":1,"state":""}`} {
		_, err = db.ListSubmissions(ctx, submissions.ListOptions{Limit: 1, After: base64.RawURLEncoding.EncodeToString([]byte(raw))})
		if !errors.Is(err, submissions.ErrInvalid) {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, limit := range []int{0, -1, 101} {
		if _, err = db.ListSubmissions(ctx, submissions.ListOptions{Limit: limit}); !errors.Is(err, submissions.ErrInvalid) {
			t.Fatalf("limit %d", limit)
		}
	}
}
