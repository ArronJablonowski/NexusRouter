package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func nativeTerminalHistory(t *testing.T, mode string) []runtime.Event {
	t.Helper()
	var journal terminalJournal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "provider", Model: "model", ModelRevision: "weights", ConfigSHA256: strings.Repeat("a", 64)}
	calls := 0
	_, _, err := runtime.RunHarness(ctx, &journal, runtime.HarnessRequest{TaskID: "task", SessionID: "task", SubmissionID: "submission", Messages: []providers.Message{{Role: "user", Content: "fixture"}}, Privacy: "local_only", ContextTokens: 8192, MaxOutputBytes: 4096, Attribution: runtime.HarnessAttribution{Identity: identity, Task: harness.TaskClass{Domain: "writing", Profile: "fixture-v1", Difficulty: "unknown"}}, Execute: func(context.Context) (runtime.HarnessOutput, error) {
		calls++
		usage := &providers.Usage{InputTokens: 3, OutputTokens: 2}
		if mode == "unknown" {
			usage = nil
		}
		if mode == "zero" {
			usage = &providers.Usage{}
		}
		output := runtime.HarnessOutput{Actual: identity, Text: "fixture answer", Usage: usage}
		if mode == "failed" {
			return output, errors.New("fixture failure")
		}
		if mode == "canceled" {
			cancel()
			return output, context.Canceled
		}
		return output, nil
	}})
	if calls != 1 || (mode != "failed" && mode != "canceled" && err != nil) {
		t.Fatal(calls, err)
	}
	return journal
}

func TestProjectNativeTerminalEvidence(t *testing.T) {
	for _, mode := range []string{"completed", "unknown", "zero", "failed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			events := nativeTerminalHistory(t, mode)
			out, err := ProjectTerminalSubmission(events)
			if err != nil || out.Result == nil {
				t.Fatal(out, err)
			}
			if mode == "failed" || mode == "canceled" {
				if out.State != mode || out.Result.Text != "" || out.Result.Usage != nil {
					t.Fatal(out)
				}
				return
			}
			if out.State != "succeeded" || out.Result.Text != "fixture answer" || out.Result.Turns != 1 || out.Result.FinishReason != "stop" || out.Result.AuditStatus != "not_recovered" {
				t.Fatal(out)
			}
			if mode == "unknown" {
				if out.Result.Usage != nil {
					t.Fatal("invented usage")
				}
			} else {
				expected := int64(3)
				if mode == "zero" {
					expected = 0
				}
				if out.Result.Usage == nil || out.Result.Usage.InputTokens != expected {
					t.Fatal(out.Result)
				}
				out.Result.Usage.InputTokens = 999
				if events[1].Data.Usage.InputTokens == 999 {
					t.Fatal("projection shares canonical usage")
				}
			}
		})
	}
}

func TestProjectNativeTerminalRejectsTampering(t *testing.T) {
	for _, mutate := range []func([]runtime.Event){
		func(e []runtime.Event) { e[1].Data.Text = "changed" },
		func(e []runtime.Event) { e[0].Data.Harness.Identity.Model = "other" },
		func(e []runtime.Event) { e[1].TurnID = "synthetic-turn" },
		func(e []runtime.Event) { e[1].Data.Usage.InputTokens = -1 },
	} {
		events := nativeTerminalHistory(t, "completed")
		mutate(events)
		if _, err := ProjectTerminalSubmission(events); err == nil {
			t.Fatal("tampered native history accepted")
		}
	}
}
