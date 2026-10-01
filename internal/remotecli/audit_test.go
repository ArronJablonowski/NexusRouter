package remotecli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func TestLocalAuditNeedsNoCredentialsOrRuntime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := remote.OpenJournal(dir, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	err = Run(context.Background(), []string{"audit", "--journal", dir, "--instance", "node-a"}, nil, &out, &stderr)
	if err != nil {
		t.Fatal(err, stderr.String())
	}
	var page remote.AuditPage
	if err = json.Unmarshal(out.Bytes(), &page); err != nil || page.Instance != "node-a" || page.Entries == nil {
		t.Fatal(page, err)
	}
	for _, extra := range [][]string{{"--through", "1"}, {"--after", "1"}, {"extra"}} {
		out.Reset()
		args := append([]string{"audit", "--journal", dir, "--instance", "node-a"}, extra...)
		if err = Run(context.Background(), args, nil, &out, &stderr); err == nil || out.Len() != 0 {
			t.Fatal("invalid scan produced output", err)
		}
	}
}

func TestLocalAuditArchivePruneRequiresBoundHash(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "journal")
	j, err := remote.OpenJournal(dir, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "remote.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO audit(at,caller,destination,action,request_id,outcome) VALUES('2026-10-01T00:00:00Z','caller','node-a','inspect','key','completed')")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "archive.json")
	common := []string{"--journal", dir, "--instance", "node-a", "--archive", archive}
	var out, stderr bytes.Buffer
	if err = Run(context.Background(), append([]string{"audit-archive"}, common...), nil, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var receipt remote.AuditArchiveReceipt
	if err = json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.Entries != 1 {
		t.Fatal(receipt, err)
	}
	out.Reset()
	if err = Run(context.Background(), append([]string{"audit-prune"}, common...), nil, &out, &stderr); err == nil || out.Len() != 0 {
		t.Fatal("prune without hash", err)
	}
	args := append([]string{"audit-prune"}, common...)
	args = append(args, "--expected", receipt.SHA256)
	if err = Run(context.Background(), args, nil, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	page, err := remote.ReadAuditPage(context.Background(), dir, "node-a", 0, 0)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Action != "audit_prune" {
		t.Fatal(page, err)
	}
}
