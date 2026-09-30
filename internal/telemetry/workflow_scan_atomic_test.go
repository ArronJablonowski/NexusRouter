package telemetry

import (
	"context"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestWorkflowScanPageRollsBackWhenHeadWriteFails(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`CREATE TRIGGER reject_scan_head BEFORE INSERT ON workflow_scans BEGIN SELECT RAISE(ABORT,'injected head failure'); END`); err != nil {
		t.Fatal(err)
	}
	page, err := s.AdvanceWorkflowScan(ctx, "project", "learning", "code", 0, 2)
	if err == nil || !reflect.DeepEqual(page, skills.WorkflowScanPage{}) {
		t.Fatal("failed write returned observation", page, err)
	}
	var pages, heads int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM workflow_scan_pages),(SELECT count(*) FROM workflow_scans)`).Scan(&pages, &heads); err != nil || pages != 0 || heads != 0 {
		t.Fatal("page committed without head", pages, heads, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_scan_head`); err != nil {
		t.Fatal(err)
	}
	first, err := s.AdvanceWorkflowScan(ctx, "project", "learning", "code", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_scan_update BEFORE UPDATE OF revision ON workflow_scans BEGIN SELECT RAISE(ABORT,'injected update failure'); END`); err != nil {
		t.Fatal(err)
	}
	page, err = s.AdvanceWorkflowScan(ctx, "project", "learning", "code", 1, 2)
	if err == nil || !reflect.DeepEqual(page, skills.WorkflowScanPage{}) {
		t.Fatal("failed update returned page", page, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_scan_pages`).Scan(&pages); err != nil || pages != 1 {
		t.Fatal("update failure left extra page", pages, err)
	}
	head, err := s.WorkflowScan(ctx, "project", "learning")
	if err != nil || head != first.Scan {
		t.Fatal("update failure moved head", head, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_scan_update`); err != nil {
		t.Fatal(err)
	}
	second, err := s.AdvanceWorkflowScan(ctx, "project", "learning", "code", 1, 2)
	if err != nil || second.Scan.Revision != 2 || second.Scan.Epoch != 2 {
		t.Fatal("retry after rollback failed", second, err)
	}
}
