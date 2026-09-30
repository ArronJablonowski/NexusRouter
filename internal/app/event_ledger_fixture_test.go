package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	_ "modernc.org/sqlite"
)

func rewriteCanonicalAppEventsForTest(t *testing.T, path, task string, selectEvent func(runtime.Event) bool, mutate func(*runtime.Event)) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT sequence,body FROM events WHERE task_id=? ORDER BY sequence`, task)
	if err != nil {
		t.Fatal(err)
	}
	type rewrite struct {
		sequence int64
		body     []byte
		digest   string
	}
	rewrites := []rewrite{}
	for rows.Next() {
		var sequence int64
		var body []byte
		var event runtime.Event
		if rows.Scan(&sequence, &body) != nil || json.Unmarshal(body, &event) != nil {
			rows.Close()
			t.Fatal("invalid event fixture")
		}
		if !selectEvent(event) {
			continue
		}
		mutate(&event)
		body, err = event.Encode()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		rewrites = append(rewrites, rewrite{sequence: sequence, body: body, digest: hex.EncodeToString(sum[:])})
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, item := range rewrites {
		if _, err = tx.Exec(`UPDATE events SET body=? WHERE task_id=? AND sequence=?`, item.body, task, item.sequence); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE event_log SET body_digest=? WHERE task_id=? AND task_sequence=?`, item.digest, task, item.sequence); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE submission_stream_events SET body_digest=? WHERE task_id=? AND task_sequence=?`, item.digest, task, item.sequence); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return len(rewrites)
}
