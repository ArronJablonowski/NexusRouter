package app

import (
	"context"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestWorkflowDiscoveryApplicationFindsVerifiedTasksWithoutPublishing(t *testing.T) {
	svc, tasks := skillGenerationAppFixture(t)
	ctx := context.Background()
	page, err := svc.DiscoverSkillWorkflows(ctx, "creative", "", 20)
	if err != nil || page.Validate("", 20) != nil || len(page.Candidates) != 2 {
		t.Fatal("accepted tasks not discovered", page, err)
	}
	ids := []string{page.Candidates[0].TaskID, page.Candidates[1].TaskID}
	sort.Strings(tasks)
	if !reflect.DeepEqual(ids, tasks) {
		t.Fatal("incorrect candidate identities", ids)
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.SkillWorkflowSources(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	for i, candidate := range page.Candidates {
		if candidate.SourceDigest != before[i].SourceDigest || candidate.EvaluationDigest != before[i].EvaluationDigest {
			t.Fatal("discovery provenance differs from actual source")
		}
	}
	// A credential collision must reject the page, not change a task identity.
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return ids[0]
		}
		return ""
	}
	denied, err := svc.DiscoverSkillWorkflows(ctx, "creative", "", 20)
	if err == nil || !reflect.DeepEqual(denied, skills.WorkflowCandidatePage{}) {
		t.Fatal("secret-bearing metadata escaped", denied, err)
	}
	after, err := db.SkillWorkflowSources(ctx, ids)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("discovery changed source history", err)
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("discovery opened skill catalog", err)
	}
	attempts, err := db.ListSkillGenerationAttempts(ctx, "project", "", 20)
	if err != nil || len(attempts) != 0 {
		t.Fatal("discovery generated a proposal", attempts, err)
	}
}
