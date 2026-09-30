package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func skillGenerationCLIRecord(t *testing.T, store *telemetry.Store, id, scope string) skills.GenerationAttempt {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := skills.GenerationAttempt{Version: 1, ID: id, Key: skills.Key{Scope: scope, Name: "workflow"}, Model: "model", Provider: "local", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"evidence-a"}, Status: "started", StartedAt: start}
	if err := store.BeginSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Status, a.FinishedAt = "drafted", start.Add(time.Second)
	a.Result = &skills.ModelDraftResult{Model: a.Model, Elapsed: time.Millisecond, Draft: skills.Draft{Key: a.Key, Description: "private-draft-marker", Steps: []string{"private-draft-marker"}, SourceSessions: a.SourceSessions, SourceEvidence: a.SourceEvidence, ValidationCases: []string{"Check fixture"}}}
	if err := store.FinishSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSkillGenerationsCLIListShowScopeAndPagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generations.db")
	store, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := skillGenerationCLIRecord(t, store, "generation-a", "project")
	skillGenerationCLIRecord(t, store, "generation-b", "project")
	skillGenerationCLIRecord(t, store, "generation-other", "other")
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"list", []string{"list", "--db", path, "--scope", "project", "--limit", "1"}, "generation-a"},
		{"next", []string{"list", "--db", path, "--scope", "project", "--after", "generation-a", "--limit", "1"}, "generation-b"},
		{"show", []string{"show", "--db", path, "--scope", "project", "--id", first.ID}, "private-draft-marker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			code := Run(append([]string{"skill-generations"}, test.args...), &out, &diagnostic, "test")
			if code != 0 || diagnostic.Len() != 0 || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), test.want) || strings.Contains(out.String(), "generation-other") {
				t.Fatalf("inspection: code=%d out=%s err=%s", code, &out, &diagnostic)
			}
			if test.name != "show" && strings.Contains(out.String(), "private-draft-marker") {
				t.Fatal("list leaked draft body")
			}
		})
	}
	var out, diagnostic bytes.Buffer
	code := Run([]string{"skill-generations", "show", "--db", path, "--scope", "other", "--id", first.ID}, &out, &diagnostic, "test")
	if code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-draft-marker") {
		t.Fatal("scope mismatch leaked result", code, &out, &diagnostic)
	}
}

func TestSkillGenerationsCLIRejectsFlagsBeforeStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-private-path.db")
	base := []string{"--db", path, "--scope", "project"}
	for _, test := range []struct {
		command string
		extra   []string
	}{
		{"list", []string{"--db", path}}, {"list", []string{"--scope=project"}}, {"list", []string{"--id", "id"}},
		{"list", []string{"--unknown", "private-secret"}}, {"list", []string{"--limit", "0"}}, {"list", []string{"--limit", "101"}}, {"list", []string{"--limit", "+1"}}, {"list", []string{"--limit", "1", "--limit=1"}},
		{"list", []string{"--after", "../private-secret"}}, {"show", nil}, {"show", []string{"--id", "id", "--after", "id"}}, {"show", []string{"--id", "id", "--limit", "1"}}, {"show", []string{"--id", "id", "--id=id"}}, {"show", []string{"--id", ""}}, {"invalid", nil},
	} {
		args := append([]string{"skill-generations", test.command}, base...)
		args = append(args, test.extra...)
		var out, diagnostic bytes.Buffer
		if code := Run(args, &out, &diagnostic, "test"); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-secret") || strings.Contains(diagnostic.String(), path) {
			t.Fatalf("invalid flags not safely rejected: %v code=%d out=%s err=%s", test, code, &out, &diagnostic)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("flag rejection touched storage", err)
		}
	}
}

func TestSkillGenerationsCLIReadOnlyMissingLegacyAndCorruption(t *testing.T) {
	for _, kind := range []string{"missing", "legacy", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "inspection.db")
			if kind != "missing" {
				store, err := telemetry.Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				skillGenerationCLIRecord(t, store, "generation-a", "project")
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
				raw, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				query := `UPDATE skill_generation_attempts SET body='private-corrupt-payload'`
				if kind == "legacy" {
					query = `DROP INDEX evaluations_routing_key; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=15`
				}
				if _, err = raw.Exec(query); err != nil {
					t.Fatal(err)
				}
				if err = raw.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for _, command := range []string{"list", "show"} {
				args := []string{"skill-generations", command, "--db", path, "--scope", "project"}
				if command == "show" {
					args = append(args, "--id", "generation-a")
				}
				var out, diagnostic bytes.Buffer
				if code := Run(args, &out, &diagnostic, "test"); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-corrupt-payload") {
					t.Fatalf("unsafe failure: %d %s %s", code, &out, &diagnostic)
				}
			}
			if kind == "missing" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("inspection created database", err)
				}
			} else if kind == "legacy" {
				raw, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				var version int
				if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 15 {
					t.Fatal("inspection migrated database", version, err)
				}
			}
		})
	}
}
