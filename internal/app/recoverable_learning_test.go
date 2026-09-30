package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestRecoverableReadFeedbackAndRestartPreserveFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	base, cfg := autoFixture(t)
	cfg.Tools.Enabled = true
	cfg.Tools.ReadRoot = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Tools.ReadRoot, "known.txt"), []byte("known fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var repaired, clean []string
	for _, repair := range []bool{true, false, true, false} {
		turns := 0
		factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
			return rotationGenerationProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
				turns++
				if turns > 1 {
					last := request.Messages[len(request.Messages)-1]
					if last.Role != "tool" || last.ToolFailed != (repair && turns == 2) {
						t.Error("failure status not delivered to repair turn")
					}
				}
				if turns == 1 || (repair && turns == 2) {
					path, id := "known.txt", "read-known"
					if repair && turns == 1 {
						path, id = "missing.txt", "read-missing"
					}
					args, _ := json.Marshal(map[string]string{"path": path})
					return emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: id, Name: "read_file", Arguments: args}, Done: true, FinishReason: "tool_calls"})
				}
				return emit(providers.Chunk{Text: "Read the known fixture.", Done: true, FinishReason: "stop"})
			}), nil
		})
		svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
		if err != nil {
			t.Fatal(err)
		}
		svc.profile = base.profile
		result, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Read the fixture", Domain: "code"})
		if err != nil || result.Text != "Read the known fixture." {
			t.Fatal("repair did not complete", err)
		}
		if err := RecordFeedback(ctx, cfg.Telemetry.Database, result.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
		if repair {
			repaired = append(repaired, result.TaskID)
		} else {
			clean = append(clean, result.TaskID)
		}
	}
	// Inspect actual execution evidence, not a fixture mutation that manufactures
	// an accepted task with a failure code.
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.Replay(ctx, db, repaired[0])
	events, readErr := db.Read(ctx, repaired[0], 0, 100)
	db.Close()
	if err != nil || readErr != nil || snapshot.State != "completed" || snapshot.UncertainEffects {
		t.Fatal("repaired durable history invalid", err)
	}
	failed := 0
	for _, message := range snapshot.Messages {
		if message.ToolFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatal("replay lost failed step", failed)
	}
	for _, event := range events {
		if event.Kind == runtime.ToolCompleted && event.Data.Code == "tool_failed" && event.Data.Effect != runtime.NoEffect {
			t.Fatal("recovered read had an uncertain or confirmed side effect")
		}
	}
	evidence, err := auditExecutionEvidence(events, nil)
	if err != nil {
		t.Fatal(err)
	}
	auditFailures := 0
	for _, item := range evidence {
		var body map[string]any
		if err := json.Unmarshal([]byte(item.Content), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] == "tool_failed" {
			auditFailures++
			if body["tool_call_id"] != "read-missing" || body["accepted"] != nil || body["text"] != nil {
				t.Fatal("audit changed failure or exposed raw output", body)
			}
		}
	}
	if auditFailures != 1 {
		t.Fatal("audit lost failed step after successful repair", auditFailures)
	}
	// A new service must pass the historical failure to a follow-up rather than
	// reconstructing it as a successful tool result.
	observed := false
	factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return rotationGenerationProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			for _, message := range request.Messages {
				observed = observed || (message.Role == "tool" && message.ToolCallID == "read-missing" && message.ToolFailed)
			}
			return emit(providers.Chunk{Text: "The first read failed before repair.", Done: true, FinishReason: "stop"})
		}), nil
	})
	restarted, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	restarted.profile = base.profile
	if _, err = restarted.Run(ctx, Request{ModelID: "a", Prompt: "What failed?", ContinueTaskID: repaired[0]}); err != nil || !observed {
		t.Fatal("restart lost historical failure", err)
	}
	restarted.settings.Skills.Enabled, restarted.settings.Skills.AutoDraft = true, true
	restarted.settings.Skills.Scope = "fixture"
	restarted.settings.Skills.Root = filepath.Join(t.TempDir(), "unpublished")
	groups, err := restarted.GroupSkillWorkflows(ctx, append(slices.Clone(repaired), clean...))
	if err != nil || len(groups) != 1 || len(groups[0].Sources) != 2 {
		t.Fatal("incorrect accepted workflow groups", groups, err)
	}
	for _, source := range groups[0].Sources {
		if !slices.Contains(clean, source.TaskID) {
			t.Fatal("positive feedback hid failed read step", source)
		}
	}
	if _, err := os.Stat(restarted.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("inspection published a skill", err)
	}
}
