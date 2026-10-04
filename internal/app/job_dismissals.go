package app

import (
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"os"
	"path/filepath"
)

// Board dismissals hide an exact unresolved observation, never execution or history.
// A new task event or renewed execution automatically makes the job visible again.
func applyJobDismissals(page *sessions.TaskPage, database string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	body, err := os.ReadFile(filepath.Join(home, ".NexusRouter", "resources", "jobs-board-dismissals.json"))
	if err != nil || len(body) > 65536 {
		return
	}
	var record struct {
		Version  int    `json:"version"`
		Database string `json:"database"`
		Items    []struct {
			TaskID   string `json:"task_id"`
			Sequence int64  `json:"sequence"`
		} `json:"items"`
	}
	if json.Unmarshal(body, &record) != nil || record.Version != 1 || record.Database != database || len(record.Items) > 100 {
		return
	}
	dismissed := map[string]int64{}
	for _, item := range record.Items {
		if !sessions.ValidEventPageID(item.TaskID) || item.Sequence < 1 {
			return
		}
		dismissed[item.TaskID] = item.Sequence
	}
	for i := range page.Items {
		item := &page.Items[i]
		if item.Execution != nil && item.Execution.State == "unknown" && dismissed[item.TaskID] == item.Sequence {
			item.Execution.Dismissed = true
		}
	}
}
