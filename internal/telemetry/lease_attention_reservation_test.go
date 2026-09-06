package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"modernc.org/sqlite"
)

func TestLeaseAttentionReservationPrecedesSnapshot(t *testing.T) {
	s, path, req := approvalFixture(t)
	ctx := context.Background()
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	if _, err := other.Exec(`PRAGMA busy_timeout=0`); err != nil {
		t.Fatal(err)
	}
	// Reproduce the original read-before-reservation ordering deterministically:
	// the second connection commits after the first has selected its WAL snapshot.
	old, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Rollback()
	var schema int
	if err := old.QueryRow(`PRAGMA user_version`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('reservation-peer','reservation-session',1,'running')`); err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`UPDATE lease_attention SET state=state WHERE 0`)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 517 {
		t.Fatalf("expected SQLITE_BUSY_SNAPSHOT(517), got %v", err)
	}
	if err := old.Rollback(); err != nil {
		t.Fatal(err)
	}
	// The production helper must acquire the write reservation before reading
	// the schema, preventing the interleaving rather than retrying stale state.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := reserveLeaseAttention(ctx, tx); err != nil {
		t.Fatal(err)
	}
	_, err = other.Exec(`UPDATE task_heads SET sequence=sequence WHERE task_id=?`, req.TaskID)
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 5 {
		t.Fatalf("expected held writer reservation SQLITE_BUSY(5), got %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`UPDATE task_heads SET sequence=sequence WHERE task_id=?`, req.TaskID); err != nil {
		t.Fatal("reservation leaked after commit", err)
	}
	var count int
	if s.db.QueryRow(`SELECT count(*) FROM lease_attention`).Scan(&count) != nil || count != 0 {
		t.Fatal("reservation inserted attention")
	}
}

func TestLeaseAttentionReservationRejectsSchemaWithoutMutation(t *testing.T) {
	for _, version := range []int{25, 27} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s, path, req := approvalFixture(t)
			insertObservedLease(t, s, "schema-token", req.TaskID, "scope", 0, req.CreatedAt.UnixNano())
			if _, err := s.db.Exec(`INSERT INTO lease_attention(id,lease_token,task_id,state,body) VALUES('schema-attention','schema-token',?,'open','preserved-body')`, req.TaskID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := reserveLeaseAttention(context.Background(), tx); err == nil {
				t.Fatal("unsupported schema reserved")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			other, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if _, err := other.Exec(`UPDATE task_heads SET sequence=sequence WHERE task_id=?`, req.TaskID); err != nil {
				t.Fatal("rejected schema leaked write lock", err)
			}
			var state, body string
			if err := other.QueryRow(`SELECT state,body FROM lease_attention WHERE id='schema-attention'`).Scan(&state, &body); err != nil || state != "open" || body != "preserved-body" {
				t.Fatal("schema rejection changed attention", err)
			}
			var got int
			if other.QueryRow(`PRAGMA user_version`).Scan(&got) != nil || got != version {
				t.Fatal("schema migrated")
			}
		})
	}
}
