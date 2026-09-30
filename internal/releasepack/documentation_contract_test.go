package releasepack

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/stateschema"
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

func TestReleaseDocumentationTracksApprovedNextVersion(t *testing.T) {
	root, err := command(t.Context(), ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	active := []string{
		"docs/release-candidate.md",
		"docs/native-target-qualification.md",
		"docs/install-migration-rehearsal.md",
		"docs/release-rollback-readiness.md",
		"docs/release-publication-authorization.md",
	}
	for _, name := range active {
		body, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		text := string(body)
		if !strings.Contains(text, "1.0.1") {
			t.Errorf("%s does not name approved next release 1.0.1", name)
		}
		if strings.Contains(text, "1.0.0") || strings.Contains(text, "v1.0.0") {
			t.Errorf("%s retains superseded active release example 1.0.0", name)
		}
	}

	packaging, err := os.ReadFile(filepath.Join(root, "docs/release-packaging.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"next release version on 2026-09-16",
		"`v1.0.0` tag is immutable release history",
		"d3dbb322c2372ed4b0b3bd9de3d7a236be006574",
		"ca07106cae194a5f02226f1e40fef0348d70f59d",
		"must use version `1.0.1` and tag `v1.0.1`",
	} {
		if !strings.Contains(string(packaging), required) {
			t.Errorf("release packaging does not preserve version decision %q", required)
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

func TestSkillSupervisionDocumentationTracksConfiguredLifecycle(t *testing.T) {
	root, err := command(t.Context(), ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	required := map[string][]string{
		"docs/skill-activation-operations.md": {
			"The configured daemon/SDK",
			"darwin_observed_tools_activation_v1",
		},
		"docs/background-learning.md": {
			"The stock CLI registers only the",
			"darwin_observed_tools_activation_v1",
		},
		"docs/skill-regression-operations.md": {
			"The separate durable named monitor persists",
			"configured daemon/SDK lifecycle can own it",
		},
		"docs/skill-regression-monitor.md": {
			"[configured daemon/SDK lifecycle](configured-learning-supervision.md)",
		},
		"docs/durable-skill-regression-monitor.md": {
			"configured daemon/SDK lifecycle can bind the monitor",
			"Outcome-based rollback uses a separate configured",
		},
		"docs/configured-learning-supervision.md": {
			"[configured supervisor](configured-outcome-supervision.md)",
		},
		"docs/skill-comparison-selection.md": {
			"[configured outcome supervisor](configured-outcome-supervision.md)",
			"durable repeated monitoring and activation-bound rollback",
		},
		"docs/skill-outcome-comparison.md": {
			"[configured outcome supervisor](configured-outcome-supervision.md)",
			"durable repeated monitoring and activation-bound rollback",
		},
		"docs/skill-outcome-rollback.md": {
			"The configured supervisor performs repeated",
			"supervisor scheduling is qualified separately",
		},
		"docs/release-notes.md": {
			"opt-in configured daemon/SDK",
			"[configured supervision](configured-learning-supervision.md)",
			"separate outcome supervisor",
		},
	}
	stale := []string{
		"Standalone daemon validator configuration and automatic regression monitoring remain open",
		"Persisted scheduling identities/fairness, standalone daemon validator configuration",
		"Standalone daemon validator configuration and cross-store power-loss",
		"subjective user-feedback weighting and standalone daemon configuration remain open",
		"daemon validation and durable monitor scheduling remain open",
		"statistical outcome regression monitoring",
		"Statistical outcome regression, safe long-term",
		"durable repeated-monitoring policy, confounder handling",
		"durable activation-bound outcome rollback are still required",
		"Workload drift and repeated monitoring remain open",
		"causal outcome attribution, or durable supervisor scheduling",
	}
	for name, phrases := range required {
		body, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		text := string(body)
		for _, phrase := range phrases {
			if !strings.Contains(text, phrase) {
				t.Errorf("%s does not track configured skill supervision %q", name, phrase)
			}
		}
		for _, phrase := range stale {
			if strings.Contains(text, phrase) {
				t.Errorf("%s retains stale skill-supervision claim %q", name, phrase)
			}
		}
	}
}
