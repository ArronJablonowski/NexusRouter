package diagnostics_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/diagnostics"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func origins() diagnostics.Origins {
	start := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Unix(100, 0), Kind: runtime.TaskStarted,
		Data: runtime.Data{ModelID: "actual:model", ProviderID: "local", Domain: "code", Profile: "default", ContextTokens: 8192}}
	turn := start
	turn.ID, turn.Sequence, turn.Kind, turn.Time, turn.TurnID, turn.AttemptID = "turn", 2, runtime.TurnStarted, time.Unix(101, 0), "turn-1", "attempt-1"
	turn.Data = runtime.Data{ModelID: "actual:model", ProviderID: "local"}
	return diagnostics.Origins{Task: start, Turn: &turn}
}

func TestDiagnosticProjectionIncludesUsefulTimingAndExplicitUnknowns(t *testing.T) {
	origin := origins()
	event := *origin.Turn
	event.ID, event.Kind, event.Sequence, event.Time = "completed", runtime.TurnCompleted, 3, time.Unix(103, 0)
	event.Data = runtime.Data{Text: "private complete answer", Usage: &providers.Usage{InputTokens: 100, OutputTokens: 20}, FinishReason: "stop"}
	metadata, err := diagnostics.Project(event, 30, origin, false)
	if err != nil || metadata.Content != nil || metadata.Model != "actual:model" || metadata.TaskElapsedMS == nil || *metadata.TaskElapsedMS != 3000 || metadata.TurnElapsedMS == nil || *metadata.TurnElapsedMS != 2000 || metadata.ContextTokens == nil || *metadata.ContextTokens != 8192 || metadata.Usage.OutputTokens != 20 {
		t.Fatal(metadata, err)
	}
	body, err := diagnostics.EncodeLine(metadata, nil)
	if err != nil || strings.Contains(string(body), "private complete answer") || !strings.Contains(string(body), `"resources":null`) {
		t.Fatal(string(body), err)
	}
	content, err := diagnostics.Project(event, 30, origin, true)
	if err != nil || content.Content == nil || content.Content.Text != event.Data.Text {
		t.Fatal("authorized complete output lost", content, err)
	}
}

func TestDiagnosticExportRedactsStructuredContentAndKeysWithoutNumberLoss(t *testing.T) {
	origin := origins()
	origin.Turn = nil
	event := origin.Task
	event.Data.Messages = []providers.Message{{Role: "user", Content: "private-key secret-token"}}
	record, err := diagnostics.Project(event, 1, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	record.Content.ToolCalls = []providers.ToolCall{{ID: "call", Name: "tool", Arguments: json.RawMessage(`{"secret-token":"private-key","large":9007199254740993}`)}}
	body, err := diagnostics.EncodeLine(record, []string{"secret-token", "private-key", ""})
	if err != nil || !json.Valid(body) || strings.Contains(string(body), "secret-token") || strings.Contains(string(body), "private-key") || !strings.Contains(string(body), "9007199254740993") {
		t.Fatal(string(body), err)
	}
	if event.Data.Messages[0].Content != "private-key secret-token" {
		t.Fatal("export rewrote immutable source")
	}
}

func TestDiagnosticRevisionAndErrorDoNotLeakBodyByDefault(t *testing.T) {
	for _, kind := range []runtime.Kind{runtime.ResponseRevision, runtime.ErrorRecorded, runtime.TaskFailed} {
		origin := origins()
		event := origin.Task
		event.Kind, event.ID, event.Sequence = kind, "later", 5
		event.Data = runtime.Data{Text: "private explanatory detail", Code: "runtime_error"}
		if kind == runtime.ResponseRevision {
			event.Data.Code = "response_contract.v1"
		}
		record, err := diagnostics.Project(event, 5, origin, false)
		if err != nil || record.Level == "info" || record.Content != nil || record.Code != event.Data.Code {
			t.Fatal(kind, record, err)
		}
	}
}

func TestDiagnosticRejectsForeignOriginAndOversizedRecords(t *testing.T) {
	origin := origins()
	foreign := origin
	foreign.Task.TaskID = "other"
	if _, err := diagnostics.Project(origin.Task, 1, foreign, false); err == nil {
		t.Fatal("foreign task origin accepted")
	}
	if _, err := diagnostics.EncodeLine(diagnostics.Record{Content: &diagnostics.Content{Text: strings.Repeat("x", diagnostics.MaxRecordBytes)}}, nil); err == nil {
		t.Fatal("oversized diagnostic output accepted")
	}
}
