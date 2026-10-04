package app

import (
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"os"
	"path/filepath"
	"testing"
)

func TestJobDismissalsOnlyHideExactUnresolvedObservation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".NexusRouter", "resources")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "jobs-board-dismissals.json"), []byte(`{"version":1,"database":"fixture","items":[{"task_id":"old","sequence":4}]}`), 0600)
	for _, tc := range []struct {
		sequence        int64
		state, database string
		hidden          bool
	}{{4, "unknown", "fixture", true}, {5, "unknown", "fixture", false}, {4, "running", "fixture", false}, {4, "unknown", "other", false}} {
		page := sessions.TaskPage{Items: []sessions.TaskSummary{{TaskID: "old", Sequence: tc.sequence, Execution: &sessions.TaskExecution{State: tc.state}}}}
		applyJobDismissals(&page, tc.database)
		if page.Items[0].Execution.Dismissed != tc.hidden {
			t.Fatal(tc)
		}
	}
}
