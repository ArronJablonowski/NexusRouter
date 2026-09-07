package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type taskListShortWriter struct{}

func (taskListShortWriter) Write(body []byte) (int, error) { return len(body) - 1, nil }

func TestTaskListCLI(t *testing.T) {
	path := taskRouteDatabase(t)
	var out, diagnostic bytes.Buffer
	if code := Run([]string{"task", "list", "--db", path, "--state", "running", "--limit", "1"}, &out, &diagnostic, "dev"); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var page sessions.TaskPage
	if json.Unmarshal(out.Bytes(), &page) != nil || page.Validate() != nil || len(page.Items) != 1 || page.Items[0].TaskID != "task" {
		t.Fatal(out.String())
	}
	if code := Run([]string{"task", "list", "--db", path, "--state", "queued"}, &out, &diagnostic, "dev"); code != 2 {
		t.Fatal("invalid task state accepted", code)
	}
	if code := Run([]string{"task", "list", "--db", path, "--db", path}, &out, &diagnostic, "dev"); code != 2 {
		t.Fatal("duplicate option accepted", code)
	}
	if code := Run([]string{"task", "list", "--db", filepath.Join(t.TempDir(), "missing.db")}, &out, &diagnostic, "dev"); code != 1 {
		t.Fatal("missing database accepted", code)
	}
	if code := Run([]string{"task", "list", "--db", path}, brokenWriter{}, &diagnostic, "dev"); code != 1 {
		t.Fatal("output failure ignored", code)
	}
	if code := Run([]string{"task", "list", "--db", path}, taskListShortWriter{}, &diagnostic, "dev"); code != 1 {
		t.Fatal("short output accepted", code)
	}
}
