package remote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func retentionFixture(t *testing.T) (*Journal, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "journal")
	j, err := OpenJournal(dir, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	for i := 0; i < 3; i++ {
		if err = j.audit(context.Background(), "caller", "inspect", "key", "completed"); err != nil {
			t.Fatal(err)
		}
	}
	return j, dir, filepath.Join(root, "archive.json")
}

func TestAuditRetentionPreservesNewEventsAndReplay(t *testing.T) {
	j, dir, path := retentionFixture(t)
	ctx := context.Background()
	if _, err := j.reserve(ctx, "caller", "key", "digest"); err != nil {
		t.Fatal(err)
	}
	if err := j.bind(ctx, "caller", "key", "submission"); err != nil {
		t.Fatal(err)
	}
	receipt, err := ArchiveAudit(ctx, dir, "node-a", path, 0)
	if err != nil || receipt.Entries != 3 || receipt.Through != 3 {
		t.Fatal(receipt, err)
	}
	if again, err := ArchiveAudit(ctx, dir, "node-a", path, 3); err != nil || again != receipt {
		t.Fatal("archive retry", again, err)
	}
	before, err := ReadAuditPage(ctx, dir, "node-a", 0, 0)
	if err != nil || len(before.Entries) != 3 {
		t.Fatal("export pruned history", err)
	}
	if err = j.audit(ctx, "caller", "dispatch", "other-key", "admitted"); err != nil {
		t.Fatal(err)
	}
	got, err := PruneAudit(ctx, dir, "node-a", path, receipt.SHA256)
	if err != nil || got.AlreadyApplied || got.AuditArchiveReceipt != receipt {
		t.Fatal(got, err)
	}
	again, err := PruneAudit(ctx, dir, "node-a", path, receipt.SHA256)
	if err != nil || !again.AlreadyApplied {
		t.Fatal("prune retry", again, err)
	}
	page, err := ReadAuditPage(ctx, dir, "node-a", 0, 0)
	if err != nil || len(page.Entries) != 2 || page.Entries[0].Sequence != 4 || page.Entries[1].Action != "audit_prune" || page.Entries[1].RequestID != receipt.SHA256 {
		t.Fatal(page, err)
	}
	if submission, err := j.reserve(ctx, "caller", "key", "digest"); err != nil || submission != "submission" {
		t.Fatal("lost replay binding", submission, err)
	}
	if _, err := j.reserve(ctx, "caller", "key", "changed"); err != ErrConflict {
		t.Fatal("lost conflict fence", err)
	}
	reopened, e := OpenJournal(dir, "node-a")
	if e != nil {
		t.Fatal(e)
	}
	if submission, e := reopened.reserve(ctx, "caller", "key", "digest"); e != nil || submission != "submission" {
		t.Fatal("restart lost replay binding", submission, e)
	}
	if e = reopened.Close(); e != nil {
		t.Fatal(e)
	}
	// A second archive retains the first prune marker, providing an archive chain.
	second, err := ArchiveAudit(ctx, dir, "node-a", path+".second", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PruneAudit(ctx, dir, "node-a", path+".second", second.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err = PruneAudit(ctx, dir, "node-a", path, receipt.SHA256); err != ErrConflict {
		t.Fatal("retired retry marker not fenced", err)
	}
}

func TestAuditRetentionRejectsTamperedIncompleteOrUnsafeArchive(t *testing.T) {
	for _, mode := range []string{"wrong_hash", "changed_record", "omitted_record", "wrong_instance", "public", "symlink", "trailing", "no_archive", "canceled", "marker_failure"} {
		t.Run(mode, func(t *testing.T) {
			j, dir, path := retentionFixture(t)
			ctx := context.Background()
			receipt, err := ArchiveAudit(ctx, dir, "node-a", path, 0)
			if err != nil {
				t.Fatal(err)
			}
			expected := receipt.SHA256
			switch mode {
			case "wrong_hash":
				expected = strings.Repeat("0", 64)
			case "changed_record":
				_, err = j.db.Exec("UPDATE audit SET outcome='denied' WHERE sequence=1")
			case "omitted_record", "wrong_instance":
				body, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				var a auditArchive
				if e = json.Unmarshal(body, &a); e != nil {
					t.Fatal(e)
				}
				if mode == "omitted_record" {
					a.Entries = a.Entries[1:]
				} else {
					a.Instance = "other"
				}
				body, e = json.Marshal(a)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(path, body, 0600)
				expected = certificateDigest(body)
			case "public":
				err = os.Chmod(path, 0644)
			case "symlink":
				link := path + ".link"
				err = os.Symlink(path, link)
				path = link
			case "trailing":
				body, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				body = append(body, '\n')
				err = os.WriteFile(path, body, 0600)
				expected = certificateDigest(body)
			case "no_archive":
				err = os.Remove(path)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "marker_failure":
				_, err = j.db.Exec("CREATE TRIGGER no_prune BEFORE INSERT ON audit WHEN NEW.action='audit_prune' BEGIN SELECT RAISE(ABORT,'fixture'); END")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = PruneAudit(ctx, dir, "node-a", path, expected); err == nil {
				t.Fatal("unsafe prune accepted")
			}
			var count int
			if err = j.db.QueryRow("SELECT count(*) FROM audit").Scan(&count); err != nil || count != 3 {
				t.Fatal("partial deletion", count, err)
			}
		})
	}
}

func TestAuditArchiveBoundedBatchesAndNoOverwrite(t *testing.T) {
	j, dir, path := retentionFixture(t)
	ctx := context.Background()
	_, err := j.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000)
 INSERT INTO audit(at,caller,destination,action,request_id,outcome)
 SELECT '2026-10-01T00:00:00Z','caller','node-a','inspect','key','completed' FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ArchiveAudit(ctx, dir, "node-a", path, 10003); err != ErrConflict {
		t.Fatal("unbounded archive", err)
	}
	receipt, err := ArchiveAudit(ctx, dir, "node-a", path, 0)
	if err != nil || receipt.Entries != 10000 || receipt.Through != 10000 {
		t.Fatal(receipt, err)
	}
	if _, err = ArchiveAudit(ctx, dir, "node-a", path, 3); err != ErrConflict {
		t.Fatal("overwrote archive", err)
	}
	if _, err = PruneAudit(ctx, dir, "node-a", path, receipt.SHA256); err != nil {
		t.Fatal(err)
	}
	page, err := ReadAuditPage(ctx, dir, "node-a", 0, 0)
	if err != nil || len(page.Entries) != 4 || page.Entries[0].Sequence != 10001 {
		t.Fatal(page, err)
	}
	missing := filepath.Join(filepath.Dir(dir), "missing")
	if _, err = PruneAudit(ctx, missing, "node-a", path, receipt.SHA256); err == nil {
		t.Fatal("created missing journal")
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
