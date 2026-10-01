package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestHarnessUsageAtomicReplayAndRestart(t *testing.T) {
	for _, mode := range []string{"completed", "failed", "canceled", "unknown", "zero"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := filepath.Join(t.TempDir(), "state.db")
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "fixture", Model: "model", ModelRevision: "revision", ConfigSHA256: strings.Repeat("a", 64)}
			usage := &providers.Usage{InputTokens: 17, OutputTokens: 4}
			if mode == "unknown" {
				usage = nil
			}
			if mode == "zero" {
				usage = &providers.Usage{}
			}
			calls := 0
			req := runtime.HarnessRequest{TaskID: "task", SessionID: "session", ContextTokens: 8192, MaxOutputBytes: 1024, Attribution: runtime.HarnessAttribution{Identity: identity, Task: harness.TaskClass{Domain: "writing", Profile: "fixture-v1", Difficulty: "easy"}}, Execute: func(context.Context) (runtime.HarnessOutput, error) {
				calls++
				output := runtime.HarnessOutput{Actual: identity, Text: "answer", Usage: usage}
				if mode == "canceled" {
					cancel()
				}
				if mode == "failed" {
					return output, errors.New("failure after measurement")
				}
				return output, nil
			}}
			_, _, err = runtime.RunHarness(ctx, s, req)
			if (err != nil) != (mode == "failed" || mode == "canceled") {
				t.Fatal(err)
			}
			ctx = context.Background()
			record, err := s.CurrentUsage(ctx, usageID(accounting.PrimaryExecution, "task"))
			if err != nil {
				t.Fatal(err)
			}
			if !accounting.SameUsage(record.Usage, usage) {
				t.Fatal("lost terminal measurement", record)
			}
			want := accounting.Completed
			if mode == "failed" {
				want = accounting.Failed
			}
			if mode == "canceled" {
				want = accounting.Canceled
			}
			if record.Disposition != want {
				t.Fatal("wrong disposition", record)
			}
			if err := s.RecordUsage(ctx, record); err != nil {
				t.Fatal(err)
			}
			changed := record
			changed.Usage = &providers.Usage{InputTokens: 99, OutputTokens: 1}
			if err := s.RecordUsage(ctx, changed); err == nil {
				t.Fatal("changed measurement accepted")
			}
			if _, _, err := runtime.RunHarness(ctx, s, req); err == nil || calls != 1 {
				t.Fatal("duplicate inference", err, calls)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			totals, err := reopened.UsageTotals(ctx, accounting.Scope{TaskID: "task"})
			if err != nil || totals.Primary.Records != 1 {
				t.Fatal("replay doubled accounting", totals, err)
			}
			if usage == nil {
				if totals.Primary.UnknownUsageRecords != 1 {
					t.Fatal("unknown became zero")
				}
			} else if totals.Primary.InputTokens == nil || *totals.Primary.InputTokens != usage.InputTokens || totals.Primary.OutputTokens == nil || *totals.Primary.OutputTokens != usage.OutputTokens {
				t.Fatal("wrong aggregate", totals)
			}
		})
	}
}

func TestHarnessUsageRejectsMixedTurnLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "fixture", Model: "model", ModelRevision: "revision", ConfigSHA256: strings.Repeat("a", 64)}
	class := harness.TaskClass{Domain: "writing", Profile: "fixture-v1", Difficulty: "easy"}
	start := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{ProviderID: id.Provider, ModelID: id.Model, ConfigID: id.ConfigSHA256, Domain: class.Domain, Profile: class.Profile, Harness: &runtime.HarnessAttribution{Identity: id, Task: class}}}
	if err := s.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	turn := start
	turn.ID = "turn"
	turn.Sequence = 2
	turn.Kind = runtime.TurnStarted
	turn.TurnID = "turn"
	turn.Data = runtime.Data{ProviderID: id.Provider, ModelID: id.Model}
	if err := s.Append(ctx, 1, turn); err != nil {
		t.Fatal(err)
	}
	terminal := start
	terminal.ID = "terminal"
	terminal.Sequence = 3
	terminal.Kind = runtime.TaskFailed
	terminal.Data = runtime.Data{Code: "harness_failed", Usage: &providers.Usage{InputTokens: 7, OutputTokens: 2}}
	if err := s.Append(ctx, 2, terminal); err == nil {
		t.Fatal("mixed accounting lifecycle accepted")
	}
	events, err := s.Read(ctx, "task", 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatal("failed projection partially committed", events, err)
	}
	if _, err := s.CurrentUsage(ctx, usageID(accounting.PrimaryExecution, "task")); err == nil {
		t.Fatal("failed append left accounting record")
	}
}
