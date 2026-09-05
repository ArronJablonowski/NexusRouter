package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestCodexHistoryShapeAdmissionPrecedesLaunch(t *testing.T) {
	for _, mode := range []string{"valid", "fresh_extra", "pending_call", "foreign_result", "unknown_role", "no_final_user", "compaction", "summary"} {
		t.Run(mode, func(t *testing.T) {
			cfg := codexTaskConfig(t)
			r := Request{ContinueTaskID: "prior"}
			messages := []providers.Message{{Role: "user", Content: "prior"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "next"}}
			switch mode {
			case "fresh_extra":
				r.ContinueTaskID = ""
			case "pending_call":
				messages[1].ToolCalls = []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{}`)}}
			case "foreign_result":
				messages = []providers.Message{{Role: "user", Content: "prior"}, {Role: "tool", ToolCallID: "unknown", Content: "answer"}, {Role: "user", Content: "next"}}
			case "unknown_role":
				messages[1].Role = "operator"
			case "no_final_user":
				messages = messages[:2]
			case "compaction":
				r.Compaction = &sessions.CompactionRequest{}
			case "summary":
				r.SummaryAttemptID = "summary"
			}
			calls := 0
			p := &codexTaskFixture{}
			r.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) { calls++; return p, nil }
			_, closeProvider, err := openTaskProvider(context.Background(), cfg, cfg.Providers[0], cfg.Models[0], r, messages, "cloud_allowed", "")
			if closeProvider != nil {
				closeProvider()
			}
			if mode == "valid" {
				if err != nil || calls != 1 || p.closed != 1 {
					t.Fatal(err, calls, p.closed)
				}
			} else if err == nil || calls != 0 {
				t.Fatal("unsupported history launched", mode, err, calls)
			}
		})
	}
}

func TestCodexSavedHistoryDenialsBeforeLaunch(t *testing.T) {
	for _, mode := range []string{"local", "legacy_privacy", "unfinished", "failed", "canceled", "missing"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			cfg := codexTaskConfig(t)
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if mode != "missing" {
				privacy := "cloud_allowed"
				if mode == "local" {
					privacy = "local_only"
				}
				if mode == "legacy_privacy" {
					privacy = ""
				}
				kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted}
				if mode == "unfinished" {
					kinds = kinds[:2]
				}
				if mode == "failed" {
					kinds[3] = runtime.TaskFailed
				}
				if mode == "canceled" {
					kinds[3] = runtime.TaskCanceled
				}
				for i, kind := range kinds {
					e := runtime.Event{Version: 1, ID: fmt.Sprintf("prior-%d", i), TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: int64(i + 1), Time: time.Now(), Kind: kind}
					if i == 0 {
						e.Data.Privacy = privacy
						e.Data.Messages = []providers.Message{{Role: "user", Content: "prior"}}
					} else {
						e.TurnID = "turn"
						e.AttemptID = "attempt"
					}
					if kind == runtime.TurnCompleted {
						e.Data.Text = "answer"
						e.Data.FinishReason = "stop"
					}
					if kind == runtime.TaskFailed {
						e.Data.Code = "failed"
					}
					if kind == runtime.TaskCanceled {
						e.Data.Code = "canceled"
					}
					if err = db.Append(ctx, int64(i), e); err != nil {
						t.Fatal(err)
					}
				}
			}
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
				t.Fatal("ineligible source launched Codex")
				return nil, nil
			}
			if _, err = svc.Run(ctx, Request{ModelID: "brain", ContinueTaskID: "prior", Prompt: "continue"}); err == nil {
				t.Fatal("ineligible continuation admitted")
			}
		})
	}
}
