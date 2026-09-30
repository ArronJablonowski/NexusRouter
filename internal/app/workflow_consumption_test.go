package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSkillWorkflowConsumptionAcrossPagesAndInspection(t *testing.T) {
	svc, _, calls := groupedAppFixture(t)
	ctx := context.Background()
	for revision := int64(0); revision < 2; revision++ {
		if _, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "code", revision, 1); err != nil {
			t.Fatal(err)
		}
	}
	first, err := svc.ConsumeSkillWorkflowScan(ctx, "learning", 0)
	if err != nil || first.Validate() != nil || len(first.Buckets) != 1 || len(first.Buckets[0].Sources) != 1 {
		t.Fatal(first, err)
	}
	second, err := svc.ConsumeSkillWorkflowScan(ctx, "learning", 1)
	if err != nil || second.Revision != 2 || len(second.Buckets) != 1 || len(second.Buckets[0].Sources) != 2 {
		t.Fatal(second, err)
	}
	retry, err := svc.ConsumeSkillWorkflowScan(ctx, "learning", 0)
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("retry changed historical receipt", retry, err)
	}
	svc.settings.Skills.AutoDraft = false
	head, err := svc.SkillWorkflowScanConsumption(ctx, "learning")
	if err != nil || !reflect.DeepEqual(head, second) {
		t.Fatal(head, err)
	}
	buckets, err := svc.SkillWorkflowScanBuckets(ctx, "learning", 1, "", 1)
	if err != nil || !reflect.DeepEqual(buckets, second.Buckets) {
		t.Fatal(buckets, err)
	}
	end, err := svc.SkillWorkflowScanBuckets(ctx, "learning", 1, buckets[0].ID, 1)
	if err != nil || end == nil || len(end) != 0 {
		t.Fatal(end, err)
	}
	if _, err := svc.ConsumeSkillWorkflowScan(ctx, "learning", 2); err == nil {
		t.Fatal("disabled drafting consumed")
	}
	if calls.Load() != 0 {
		t.Fatal("consumption invoked inference")
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("consumption opened skill root", err)
	}
}

func TestSkillWorkflowConsumptionSecretGuardsBeforeAndAfterCommit(t *testing.T) {
	for _, phase := range []string{"initial", "guard", "post-commit"} {
		t.Run(phase, func(t *testing.T) {
			svc, tasks, _ := groupedAppFixture(t)
			ctx := context.Background()
			if _, err := svc.AdvanceSkillWorkflowScan(ctx, "learning", "code", 0, 20); err != nil {
				t.Fatal(err)
			}
			lookups := 0
			svc.secret = func(name string) string {
				if name != "DARWIN_API_TOKEN" {
					return ""
				}
				lookups++
				if phase == "initial" && lookups == 1 || phase == "guard" && lookups == 2 || phase == "post-commit" && lookups >= 3 {
					return tasks[0]
				}
				return "unrelated-key"
			}
			got, err := svc.ConsumeSkillWorkflowScan(ctx, "learning", 0)
			if err == nil || !reflect.DeepEqual(got, skills.WorkflowScanConsumption{}) {
				t.Fatal("credential leaked", got, err)
			}
			raw, err := sql.Open("sqlite", "file:"+svc.settings.Telemetry.Database+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var count int
			want := 0
			if phase == "post-commit" {
				want = 1
			}
			if err := raw.QueryRow("SELECT count(*) FROM workflow_scan_consumptions").Scan(&count); err != nil || count != want {
				t.Fatal("unexpected commit", count, err)
			}
			svc.secret = nil
			receipt, err := svc.ConsumeSkillWorkflowScan(ctx, "learning", 0)
			if err != nil || receipt.Revision != 1 {
				t.Fatal("retry advanced", receipt, err)
			}
			svc.secret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return tasks[0]
				}
				return ""
			}
			if _, err := svc.ConsumeSkillWorkflowScan(ctx, "learning", 0); err == nil {
				t.Fatal("historical receipt leaked")
			}
			if _, err := svc.SkillWorkflowScanConsumption(ctx, "learning"); err == nil {
				t.Fatal("head receipt leaked")
			}
			if buckets, err := svc.SkillWorkflowScanBuckets(ctx, "learning", 1, "", 20); err == nil || buckets != nil {
				t.Fatal("buckets leaked", buckets, err)
			}
		})
	}
}

func TestSkillWorkflowConsumptionAdmissionDoesNotInitialize(t *testing.T) {
	svc, _, _ := selectionAppFixture(t)
	svc.settings.Telemetry.Database = filepath.Join(t.TempDir(), "absent", "state.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, service := range []*Service{nil, svc} {
		for _, callCtx := range []context.Context{nil, ctx, context.Background()} {
			if _, err := service.ConsumeSkillWorkflowScan(callCtx, "learning", 0); err == nil {
				t.Fatal("invalid admission consumed")
			}
			if _, err := service.SkillWorkflowScanConsumption(callCtx, "learning"); err == nil {
				t.Fatal("invalid admission read")
			}
			if _, err := service.SkillWorkflowScanBuckets(callCtx, "learning", 1, "", 20); err == nil {
				t.Fatal("invalid admission listed")
			}
		}
	}
	if _, err := os.Stat(filepath.Dir(svc.settings.Telemetry.Database)); !os.IsNotExist(err) {
		t.Fatal("created store directory", err)
	}
	for _, cursor := range []string{"bad", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if workflowBucketCursor(cursor) {
			t.Fatal("invalid cursor admitted")
		}
	}
}
