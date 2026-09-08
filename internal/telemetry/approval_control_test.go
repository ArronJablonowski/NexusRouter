package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
)

func TestApprovalControlCurrentDatabaseDecision(t *testing.T) {
	s, path, req := approvalFixture(t)
	ctx := context.Background()
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	control, err := OpenApprovalControl(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	r, err := control.DecideBoundApproval(ctx, approvals.Command{Expected: req, ID: "external", Allowed: true}, "operator", req.CreatedAt.Add(time.Second))
	if err != nil || r.State != approvals.Approved {
		t.Fatal(r, err)
	}
	r, err = s.ReadApproval(ctx, req.ID)
	if err != nil || r.State != approvals.Approved {
		t.Fatal("decision not durable", r, err)
	}
}

func TestApprovalControlRejectsWithoutCreationOrMigration(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"missing", "directory", "empty", "invalid", "legacy", "future", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-control.db")
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "empty", "invalid":
				body := []byte{}
				if kind == "invalid" {
					body = []byte("private invalid database")
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			case "legacy", "future", "rollback":
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				version := "14"
				if kind == "future" {
					version = "31"
				}
				if kind == "rollback" {
					version = "26"
				}
				if _, err := db.Exec(`CREATE TABLE preserved(value TEXT); INSERT INTO preserved VALUES('unchanged'); PRAGMA user_version=` + version); err != nil {
					t.Fatal(err)
				}
				if kind != "rollback" {
					if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			store, err := OpenApprovalControl(ctx, path)
			if store != nil {
				store.Close()
				t.Fatal("unsupported control opened")
			}
			if err != approvals.ErrUnavailable || err.Error() != approvals.ErrUnavailable.Error() {
				t.Fatal("non-generic rejection", err)
			}
			if kind == "missing" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("missing database created", err)
				}
				return
			}
			if kind == "directory" {
				return
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected database changed", err)
			}
		})
	}
}

func TestApprovalControlCanceledContext(t *testing.T) {
	_, path, _ := approvalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := OpenApprovalControl(ctx, path)
	if s != nil {
		s.Close()
		t.Fatal("canceled control opened")
	}
	if err != approvals.ErrUnavailable {
		t.Fatal(err)
	}
}
