package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestSessionTasksCLIIsReadOnlyAndBound(t *testing.T) {
	path := taskRouteDatabase(t)
	var out, diagnostic bytes.Buffer
	args := []string{"session", "tasks", "--db", path, "--session", "session", "--limit", "1"}
	if code := Run(args, &out, &diagnostic, "dev"); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var page sessions.SessionTaskPage
	if json.Unmarshal(out.Bytes(), &page) != nil || page.Validate() != nil || page.SessionID != "session" || len(page.Items) != 1 || page.Items[0].TaskID != "task" {
		t.Fatal(out.String())
	}
	if Run(args, taskListShortWriter{}, &diagnostic, "dev") != 1 {
		t.Fatal("short output accepted")
	}
	absent := filepath.Join(t.TempDir(), "absent.db")
	if Run([]string{"session", "tasks", "--db", absent, "--session", "session"}, &out, &diagnostic, "dev") != 1 {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatal("inspection created database", err)
	}
}

func TestSessionTasksCLIRejectsAmbiguousInput(t *testing.T) {
	path := taskRouteDatabase(t)
	var out, diagnostic bytes.Buffer
	for _, args := range [][]string{
		{"session"},
		{"session", "show"},
		{"session", "tasks", "--db", path},
		{"session", "tasks", "--db", path, "--session", "bad:id"},
		{"session", "tasks", "--db", path, "--session", "session", "--db", path},
		{"session", "tasks", "--db", path, "--session", "session", "trailing"},
	} {
		if code := Run(args, &out, &diagnostic, "dev"); code != 2 {
			t.Fatalf("accepted %#v: %d", args, code)
		}
	}
}
