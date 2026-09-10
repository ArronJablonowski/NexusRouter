package telemetry

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSameDatabaseFileUsesExactFilesystemIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if same, err := store.SameDatabaseFile(ctx, path); err != nil || !same {
		t.Fatalf("exact path same=%v err=%v", same, err)
	}
	alias := filepath.Join(dir, "state-alias.db")
	if err = os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if same, err := store.SameDatabaseFile(ctx, alias); err != nil || !same {
		t.Fatalf("symlink alias same=%v err=%v", same, err)
	}
	otherPath := filepath.Join(dir, "other.db")
	other, err := Open(ctx, otherPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if same, err := store.SameDatabaseFile(ctx, otherPath); err != nil || same {
		t.Fatalf("different store same=%v err=%v", same, err)
	}
}

func TestSameDatabaseFileRejectsCopyWithPortableWorkspaceIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "source.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.WorkspaceIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(dir, "copy.db")
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	copyFile, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(copyFile, source)
	closeCopyErr := copyFile.Close()
	closeSourceErr := source.Close()
	if copyErr != nil || closeCopyErr != nil || closeSourceErr != nil {
		t.Fatalf("copy errors: %v %v %v", copyErr, closeCopyErr, closeSourceErr)
	}
	copied, err := Open(ctx, copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	copiedIdentity, err := copied.WorkspaceIdentity(ctx)
	if err != nil || copiedIdentity != identity {
		t.Fatalf("copy did not preserve workspace identity: %q %q err=%v", identity, copiedIdentity, err)
	}
	if same, err := store.SameDatabaseFile(ctx, copyPath); err != nil || same {
		t.Fatalf("copied database same=%v err=%v", same, err)
	}
}

func TestSameDatabaseFileFailsClosedForClosedStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "closed.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if same, err := store.SameDatabaseFile(ctx, path); err == nil || same {
		t.Fatalf("closed store same=%v err=%v", same, err)
	}
}
