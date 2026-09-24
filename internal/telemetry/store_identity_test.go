package telemetry

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidatedStoreIdentityRejectsChangedStorage(t *testing.T) {
	for _, mode := range []string{"schema", "version", "journal", "replacement", "closed", "cancelled", "wrong_path"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.ValidateIdentity(ctx, path); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "schema":
				_, err = store.db.Exec("CREATE TABLE unexpected(value TEXT)")
			case "journal":
				_, err = store.db.Exec("PRAGMA journal_mode=DELETE")
			case "version":
				_, err = store.db.Exec("PRAGMA user_version=999")
			case "replacement":
				if err = os.Rename(path, path+".old"); err == nil {
					err = os.WriteFile(path, []byte("replacement"), 0600)
				}
			case "closed":
				err = store.Close()
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "wrong_path":
				path += ".other"
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = store.ValidateIdentity(ctx, path); err == nil {
				t.Fatal("stale storage accepted")
			}
		})
	}
}

func TestValidatedStoreIdentityAcceptsOrdinaryWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// A data commit must not invalidate a long-lived handle; schema changes do.
	if _, err = other.db.Exec("UPDATE submissions SET id=id WHERE id=''"); err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateIdentity(ctx, path); err != nil {
		t.Fatal(err)
	}
}

func TestValidatedStoreReplacementConnectionKeepsSafetySettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(any) error { return driver.ErrBadConn })
	if !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("discard connection: %v", err)
	}
	_ = conn.Close()
	if err := s.ValidateIdentity(ctx, path); err != nil {
		t.Fatal(err)
	}
	for pragma, want := range map[string]int{"foreign_keys": 1, "synchronous": 2, "busy_timeout": 5000} {
		var got int
		if err := s.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("%s=%d want=%d err=%v", pragma, got, want, err)
		}
	}
}
