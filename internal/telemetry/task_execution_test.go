package telemetry

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"testing"
	"time"
)

func TestJobExecutionDoesNotConfuseHistoricalHeadsWithRunningWork(t *testing.T) {
	ctx := context.Background()
	db, _ := submissionStore(t)
	now := time.Now().UTC()
	for _, id := range []string{"failed", "canceled", "unowned", "expired", "active"} {
		appendListedTask(t, db, id, "chat-"+id, "running", now.Add(-time.Hour))
	}
	for _, state := range []string{"failed", "canceled"} {
		sub := queuedSubmission(t, db, state)
		if _, err := db.db.Exec(`UPDATE submissions SET state=? WHERE id=?`, state, sub.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`UPDATE events SET body=json_set(body,'$.data.submission_id',?) WHERE task_id=? AND sequence=1`, sub.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"expired", "active"} {
		expiry := now.Add(time.Minute)
		if id == "expired" {
			expiry = now.Add(-time.Minute)
		}
		if _, err := db.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES(?,?,?,?,1,?,0)`, id, id, "fixture", "fixture", expiry.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.ListTasks(ctx, sessions.TaskListOptions{State: "running", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ObserveTaskExecution(ctx, &page, now); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"failed": "failed", "canceled": "canceled", "unowned": "unknown", "expired": "unknown", "active": "running"}
	for _, item := range page.Items {
		if item.Execution == nil || item.Execution.State != expected[item.TaskID] || item.State != "running" {
			t.Fatal(item)
		}
	}
	if err = db.ObserveTaskExecution(ctx, &page, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.TaskID == "active" && item.Execution.State != "unknown" {
			t.Fatal("expired lease stayed running")
		}
	}
	var count int
	if err = db.db.QueryRow(`SELECT count(*) FROM task_heads WHERE state='running'`).Scan(&count); err != nil || count != 5 {
		t.Fatal("observation mutated history", count, err)
	}
}
