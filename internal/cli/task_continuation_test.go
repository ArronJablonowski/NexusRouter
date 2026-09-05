package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestTaskContinuationMetadataOnly(t *testing.T) {
	for _, mode := range []string{"recovered", "completed", "failed", "canceled", "pending", "interrupted", "uncertain", "wrong-cause"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "task.db")
			db, err := telemetry.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.TaskFailed}
			events := make([]runtime.Event, len(kinds))
			for i, kind := range kinds {
				events[i] = runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind, TurnID: "turn", AttemptID: "attempt"}
			}
			events[0].Data.Messages = []providers.Message{{Role: "user", Content: "private-prompt-marker"}}
			events[2].Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"private-argument-marker","validation":"text"}`)}}
			for _, i := range []int{3, 4} {
				events[i].Data.ToolCallID, events[i].Data.ToolName = "call", "delegate"
				events[i].Data.Effect = runtime.NoEffect
			}
			events[4].Data.Code, events[4].Data.Effect, events[4].Data.Text = "delegation_recovered", runtime.NoEffect, "private-output-marker"
			events[5].Data.Code, events[5].CausationID = "interrupted_after_delegation", events[4].ID
			switch mode {
			case "completed":
				events[5].Kind, events[5].Data.Code = runtime.TaskCompleted, ""
			case "failed":
				events[5].Data.Code = "execution_failed"
			case "canceled":
				events[5].Kind, events[5].Data.Code = runtime.TaskCanceled, "canceled"
			case "pending":
				events = events[:4]
			case "interrupted":
				events = events[:2]
			case "uncertain":
				events[4].Data.Effect = runtime.UncertainEffect
			case "wrong-cause":
				events[5].CausationID = "foreign"
			}
			for i, event := range events {
				if err := db.Append(ctx, int64(i), event); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			var out, errout bytes.Buffer
			args := []string{"task", "continuation", "--db", path, "--task", "task"}
			if code := Run(args, &out, &errout, "dev"); code != 0 {
				t.Fatal(code, errout.String())
			}
			var status sessions.ContinuationStatus
			if json.Unmarshal(out.Bytes(), &status) != nil || status.Validate() != nil || status.TaskID != "task" || status.Sequence != int64(len(events)) || status.HistoryEligible != (mode == "recovered" || mode == "completed") {
				t.Fatal("wrong continuation readiness", out.String())
			}
			if mode == "recovered" && status.Reason != "recovered_delegation" {
				t.Fatal(status)
			}
			if strings.Contains(out.String(), "private-") || errout.Len() != 0 {
				t.Fatal("inspection leaked payload")
			}
			if Run(args, brokenWriter{}, &errout, "dev") != 1 {
				t.Fatal("ignored output failure")
			}
			ro, err := telemetry.OpenReadOnly(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			after, err := ro.Read(ctx, "task", 0, 100)
			if err != nil || !reflect.DeepEqual(events, after) {
				t.Fatal("inspection changed journal", err)
			}
		})
	}
}

func TestTaskContinuationRejectsInvalidRequestsWithoutStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	for _, flags := range [][]string{{"--db", path}, {"--db", path, "--task", "task", "--task", "other"}, {"--db", path, "--task", "bad:id"}, {"--db", path, "--task", "task", "--extra", "private-value"}} {
		var out, errout bytes.Buffer
		if Run(append([]string{"task", "continuation"}, flags...), &out, &errout, "dev") != 2 || out.Len() != 0 || strings.Contains(errout.String(), "private-value") {
			t.Fatal("invalid arguments accepted")
		}
	}
	var out, errout bytes.Buffer
	if Run([]string{"task", "continuation", "--db", path, "--task", "task"}, &out, &errout, "dev") != 1 || out.Len() != 0 {
		t.Fatal("missing history accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created storage", err)
	}
}
