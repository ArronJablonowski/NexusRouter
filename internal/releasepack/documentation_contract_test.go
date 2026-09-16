package releasepack

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

func TestReleaseDocumentationTracksArchiveAndCurrentSchema(t *testing.T) {
	root, err := command(t.Context(), ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	current := strconv.Itoa(stateschema.Current)
	checks := map[string][]string{
		"docs/release-notes.md": {
			"current durable store uses SQLite schema " + current + ".",
		},
		"docs/install-migration-rehearsal.md": {
			"seven-entry release archive",
			"and schema " + current + ".",
			"from schema\n29 to " + current,
			"--current-schema " + current,
			"including current schema " + current,
		},
		"docs/release-rollback-readiness.md": {
			"(currently `" + current + "`)",
			"--current-schema " + current,
		},
	}
	for name, required := range checks {
		body, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, phrase := range required {
			if !strings.Contains(string(body), phrase) {
				t.Errorf("%s does not track release contract %q", name, phrase)
			}
		}
	}
}

func TestInitialHybridGuideTracksCodexRolloverContract(t *testing.T) {
	root, err := command(t.Context(), ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "docs/initial-hybrid-test.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"A reviewed version-two compaction plan can roll a completed native",
		"[plan-backed rollover contract](codex-coordinator-integration.md#plan-backed-context-rollover)",
		"This is host-directed durable rollover, not provider-native automatic",
		"a paused tool call remains ineligible",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("initial hybrid guide does not track Codex rollover contract %q", required)
		}
	}
	if stale := "health/routing discovery and compaction remain unsupported"; strings.Contains(text, stale) {
		t.Errorf("initial hybrid guide retains stale Codex rollover claim %q", stale)
	}
}
