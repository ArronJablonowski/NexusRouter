package remote

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAuditInspectionStablePaginationPreservesReplay(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := OpenJournal(dir, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err = j.reserve(ctx, "caller", "request", "digest"); err != nil {
		t.Fatal(err)
	}
	if err = j.bind(ctx, "caller", "request", "submission"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		if err = j.audit(ctx, "caller", "inspect", "request", "admitted"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := ReadAuditPage(ctx, dir, "node-a", 0, 0)
	if err != nil || first.Through != 205 || first.Next != 100 || len(first.Entries) != 100 {
		t.Fatal(first, err)
	}
	// A live writer advances while an administrator exports the earlier prefix.
	if err = j.audit(ctx, "caller", "cancel", "request", "completed"); err != nil {
		t.Fatal(err)
	}
	second, err := ReadAuditPage(ctx, dir, "node-a", first.Next, first.Through)
	if err != nil || second.Next != 200 || len(second.Entries) != 100 {
		t.Fatal(second, err)
	}
	last, err := ReadAuditPage(ctx, dir, "node-a", second.Next, first.Through)
	if err != nil || last.Next != 0 || len(last.Entries) != 5 || last.Entries[4].Sequence != 205 {
		t.Fatal(last, err)
	}
	repeated, err := ReadAuditPage(ctx, dir, "node-a", 0, first.Through)
	if err != nil || !reflect.DeepEqual(first, repeated) {
		t.Fatal("prefix changed", err)
	}
	fresh, err := ReadAuditPage(ctx, dir, "node-a", 205, 206)
	if err != nil || len(fresh.Entries) != 1 || fresh.Entries[0].Action != "cancel" {
		t.Fatal(fresh, err)
	}
	if submission, err := j.reserve(ctx, "caller", "request", "digest"); err != nil || submission != "submission" {
		t.Fatal("replay identity changed", submission, err)
	}
	if _, err = j.reserve(ctx, "caller", "request", "other-digest"); err != ErrConflict {
		t.Fatal("replay conflict lost", err)
	}
}

func TestAuditInspectionRejectsUnsafeOrInconsistentSources(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if _, err := ReadAuditPage(ctx, missing, "node-a", 0, 0); err == nil {
		t.Fatal("created missing journal")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read created directory", err)
	}
	dir := filepath.Join(root, "journal")
	j, err := OpenJournal(dir, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	empty, err := ReadAuditPage(ctx, dir, "node-a", 0, 0)
	if err != nil || empty.Through != 0 || empty.Entries == nil {
		t.Fatal(empty, err)
	}
	for _, args := range []struct {
		instance       string
		after, through int64
	}{{"other", 0, 0}, {"node-a", -1, 0}, {"node-a", 1, 0}, {"node-a", 0, -1}, {"node-a", 0, 1}} {
		if _, err := ReadAuditPage(ctx, dir, args.instance, args.after, args.through); err == nil {
			t.Fatal("accepted invalid cursor or identity", args)
		}
	}
	link := filepath.Join(root, "link")
	if err = os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadAuditPage(ctx, link, "node-a", 0, 0); err == nil {
		t.Fatal("accepted symlink")
	}
	if err = os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadAuditPage(ctx, dir, "node-a", 0, 0); err == nil {
		t.Fatal("accepted public directory")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = j.audit(ctx, "caller", "inspect", "request", "completed"); err != nil {
		t.Fatal(err)
	}
	if _, err = j.db.Exec("UPDATE audit SET destination='other'"); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadAuditPage(ctx, dir, "node-a", 0, 0); err == nil {
		t.Fatal("accepted wrong destination")
	}
	if _, err = j.db.Exec("UPDATE audit SET destination='node-a', outcome=zeroblob(1000000)"); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadAuditPage(ctx, dir, "node-a", 0, 0); err == nil {
		t.Fatal("accepted oversized record")
	}
	if _, err = j.db.Exec("UPDATE audit SET outcome=?", "\x00"+strings.Repeat("x", 1000000)); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadAuditPage(ctx, dir, "node-a", 0, 0); err == nil {
		t.Fatal("NUL bypassed byte bound")
	}

}
