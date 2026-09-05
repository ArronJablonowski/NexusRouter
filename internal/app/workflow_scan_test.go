package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSkillWorkflowScanSavedRetryAndReadOnlyInspection(t *testing.T) {
	svc, _, calls := selectionAppFixture(t)
	ctx := context.Background()
	first, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 1)
	if err != nil || first.Validate() != nil || first.Scan.Revision != 1 || first.Scan.Complete || calls.Load() != 0 {
		t.Fatal(first, err, calls.Load())
	}
	second, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 1, 1)
	if err != nil || second.Scan.Revision != 2 || !second.Scan.Complete {
		t.Fatal(second, err)
	}
	retry, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 1)
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("retry advanced", retry, err)
	}
	svc.settings.Skills.AutoDraft = false
	head, err := svc.SkillWorkflowScan(ctx, "learning")
	if err != nil || head != second.Scan {
		t.Fatal(head, err)
	}
	if _, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 2, 1); err == nil {
		t.Fatal("disabled advancement admitted")
	}
	if calls.Load() != 0 {
		t.Fatal("scan invoked model", calls.Load())
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("scan opened skills", err)
	}
}

func TestSkillWorkflowScanRejectsCredentialsBeforePersistence(t *testing.T) {
	for _, rotation := range []string{"constant", "initial-only", "guard-only"} {
		t.Run(rotation, func(t *testing.T) {
			svc, tasks, calls := selectionAppFixture(t)
			lookups := 0
			svc.secret = func(name string) string {
				if name != "DARWIN_API_TOKEN" {
					return ""
				}
				lookups++
				if rotation == "constant" || rotation == "initial-only" && lookups == 1 || rotation == "guard-only" && lookups == 2 {
					return tasks[0]
				}
				return "unrelated-key"
			}
			page, err := svc.AdvanceSkillWorkflowScan(context.Background(), "learning", "creative", 0, 20)
			if err == nil || !reflect.DeepEqual(page, skills.WorkflowScanPage{}) || calls.Load() != 0 {
				t.Fatal("credential admitted", page, err)
			}
			workflowScanAppCounts(t, svc, 0, 0)
			svc.secret = nil
			page, err = svc.AdvanceSkillWorkflowScan(context.Background(), "learning", "creative", 0, 20)
			if err != nil || page.Scan.Revision != 1 {
				t.Fatal("rejection moved cursor", page, err)
			}
		})
	}
}

func TestSkillWorkflowScanGuardsHistoricalReadWithoutRewriting(t *testing.T) {
	svc, tasks, _ := selectionAppFixture(t)
	ctx := context.Background()
	first, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return tasks[0]
		}
		return ""
	}
	if page, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 20); err == nil || !reflect.DeepEqual(page, skills.WorkflowScanPage{}) {
		t.Fatal("historical metadata leaked", page, err)
	}
	workflowScanAppCounts(t, svc, 1, 1)
	svc.secret = nil
	retry, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 20)
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("rejected read changed record", retry, err)
	}
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return first.Scan.Upper
		}
		return ""
	}
	if head, err := svc.SkillWorkflowScan(ctx, "learning"); err == nil || !reflect.DeepEqual(head, skills.WorkflowScan{}) {
		t.Fatal("head metadata leaked", head, err)
	}
}

func TestSkillWorkflowScanPostCommitRejectionKeepsRetryIdentity(t *testing.T) {
	svc, tasks, _ := selectionAppFixture(t)
	lookups := 0
	svc.secret = func(name string) string {
		if name != "DARWIN_API_TOKEN" {
			return ""
		}
		lookups++
		if lookups >= 3 {
			return tasks[0]
		}
		return "earlier-key"
	}
	page, err := svc.AdvanceSkillWorkflowScan(context.Background(), "learning", "creative", 0, 20)
	if err == nil || !reflect.DeepEqual(page, skills.WorkflowScanPage{}) {
		t.Fatal("post-commit credential leaked", page, err)
	}
	workflowScanAppCounts(t, svc, 1, 1)
	svc.secret = nil
	retry, err := svc.AdvanceSkillWorkflowScan(context.Background(), "learning", "creative", 0, 20)
	if err != nil || retry.Scan.Revision != 1 {
		t.Fatal("lost response moved scan twice", retry, err)
	}
	workflowScanAppCounts(t, svc, 1, 1)
}

func TestSkillWorkflowScanAdmissionDoesNotInitializeOrMigrate(t *testing.T) {
	svc, _, _ := selectionAppFixture(t)
	ctx := context.Background()
	database := svc.settings.Telemetry.Database
	svc.settings.Telemetry.Database = filepath.Join(t.TempDir(), "absent", "state.db")
	if _, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 20); err == nil {
		t.Fatal("missing store admitted")
	}
	if _, err := svc.SkillWorkflowScan(ctx, "learning"); err == nil {
		t.Fatal("missing store inspected")
	}
	if _, err := os.Stat(filepath.Dir(svc.settings.Telemetry.Database)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created store directory", err)
	}
	svc.settings.Telemetry.Database = database
	raw, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("PRAGMA user_version=17"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 20); err == nil {
		t.Fatal("legacy store admitted")
	}
	var schema int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != 17 {
		t.Fatal("legacy store migrated", schema, err)
	}
	if _, err := raw.Exec("PRAGMA user_version=19"); err != nil {
		t.Fatal(err)
	}
	for _, service := range []*Service{nil, svc} {
		if _, err := service.AdvanceSkillWorkflowScan(nil, "learning", "creative", 0, 20); err == nil {
			t.Fatal("nil context admitted")
		}
		if _, err := service.SkillWorkflowScan(nil, "learning"); err == nil {
			t.Fatal("nil context inspected")
		}
	}
	for _, test := range []struct {
		name, domain string
		revision     int64
		limit        int
	}{{"bad name", "creative", 0, 20}, {"learning", "bad domain", 0, 20}, {"learning", "creative", -1, 20}, {"learning", "creative", 1_000_000_000, 20}, {"learning", "creative", 0, 21}} {
		if _, err := svc.AdvanceSkillWorkflowScan(ctx, test.name, test.domain, test.revision, test.limit); err == nil {
			t.Fatal("invalid input admitted", test)
		}
	}
	workflowScanAppCounts(t, svc, 0, 0)
}

func workflowScanAppCounts(t *testing.T, svc *Service, heads, pages int) {
	t.Helper()
	store, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if heads == 0 {
		if _, err := store.WorkflowScan(context.Background(), "project", "learning"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("rejected scan has head", err)
		}
	}
	raw, err := sql.Open("sqlite", "file:"+svc.settings.Telemetry.Database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var actualHeads, actualPages int
	if err := raw.QueryRow(`SELECT (SELECT count(*) FROM workflow_scans),(SELECT count(*) FROM workflow_scan_pages)`).Scan(&actualHeads, &actualPages); err != nil || actualHeads != heads || actualPages != pages {
		t.Fatal(actualHeads, actualPages, err)
	}
}
