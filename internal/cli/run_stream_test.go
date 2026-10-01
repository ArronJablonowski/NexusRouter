package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestRunJSONFlagParsingPreservesFlagValues(t *testing.T) {
	base := []string{"--config", "project.yaml", "--model", "auto"}
	for _, tc := range []struct {
		flags []string
		want  bool
	}{
		{nil, false}, {[]string{"--json"}, true}, {[]string{"--json=true"}, true}, {[]string{"--json=false"}, false},
		{[]string{"--json=true", "--json=false"}, false}, {[]string{"--json=false", "--json"}, true},
	} {
		options, req, mode, err := parseRunOptions(append(append([]string{}, base...), tc.flags...))
		if err != nil || mode != tc.want || options.ProjectFile != "project.yaml" || req.ModelID != "auto" {
			t.Fatal(tc, options, req, mode, err)
		}
	}
	for _, tc := range []struct {
		args                    []string
		project, model, profile string
	}{
		{[]string{"--config", "--json", "--model", "auto"}, "--json", "auto", ""},
		{[]string{"--config", "project.yaml", "--model", "--json"}, "project.yaml", "--json", ""},
		{[]string{"--config", "project.yaml", "--model", "auto", "--profile", "--json=false"}, "project.yaml", "auto", "--json=false"},
	} {
		options, req, mode, err := parseRunOptions(tc.args)
		if err != nil || mode || options.ProjectFile != tc.project || req.ModelID != tc.model || req.Profile != tc.profile {
			t.Fatal("flag value mistaken for JSON switch", tc, options, req, mode, err)
		}
	}
	for _, flags := range [][]string{{"--json=not-a-bool"}, {"--json="}, {"--json", "true"}, {"--json", "false"}} {
		if _, _, _, err := parseRunOptions(append(append([]string{}, base...), flags...)); err == nil {
			t.Fatal("malformed bool accepted", flags)
		}
	}
	var out, diagnostic bytes.Buffer
	if code := RunWithInput([]string{"run", "--config", "missing.yaml", "--model", "auto", "--json=private-parser-value"}, strings.NewReader("prompt"), &out, &diagnostic, "test"); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-parser-value") {
		t.Fatal("unsafe parse error", code, out.String(), diagnostic.String())
	}
}

func cliStreamEvent(sequence int64, kind runtime.Kind) runtime.Event {
	return runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: sequence, Time: time.Unix(100, 0).UTC(), Kind: kind}
}

func readJSONLines(t *testing.T, raw string) []map[string]json.RawMessage {
	t.Helper()
	if !strings.HasSuffix(raw, "\n") {
		t.Fatalf("unterminated JSON line: %q", raw)
	}
	var result []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		var value map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &value) != nil || value == nil || string(value["version"]) != "1" {
			t.Fatalf("invalid versioned JSON line: %q", line)
		}
		result = append(result, value)
	}
	return result
}

func TestRunTaskJSONEmitsEventsAndSnakeCaseResultOnly(t *testing.T) {
	events := []runtime.Event{cliStreamEvent(1, runtime.TaskStarted), cliStreamEvent(2, runtime.TaskCompleted)}
	events[1].Data.Text = "answer\nsecond line"
	request := app.Request{ModelID: "auto", Prompt: "hello"}
	routeCost := .75
	var out, diagnostic bytes.Buffer
	code := runTaskJSON(context.Background(), request, func(_ context.Context, got app.Request, emit func(runtime.Event) error) (app.Result, error) {
		if !reflect.DeepEqual(got, request) {
			t.Fatal("request changed", got)
		}
		for _, event := range events {
			if err := emit(event); err != nil {
				return app.Result{}, err
			}
		}
		return app.Result{TaskID: "task", Text: "answer\nsecond line", Turns: 1, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 3, OutputTokens: 4}, PreviousTaskIDs: []string{"prior"}, RouteEstimatedCost: &routeCost, AuditID: "audit", AuditStatus: "recorded"}, nil
	}, &out, &diagnostic)
	if code != 0 || diagnostic.Len() != 0 {
		t.Fatal(code, diagnostic.String())
	}
	lines := readJSONLines(t, out.String())
	if len(lines) != 3 {
		t.Fatal("plaintext or extra records in stream", out.String())
	}
	for i, event := range events {
		var got runtime.Event
		if string(lines[i]["type"]) != `"event"` || len(lines[i]) != 3 || json.Unmarshal(lines[i]["event"], &got) != nil || !reflect.DeepEqual(got, event) {
			t.Fatal("event envelope changed", lines[i], got)
		}
	}
	if string(lines[2]["type"]) != `"result"` || len(lines[2]) != 3 {
		t.Fatal("invalid result envelope", lines[2])
	}
	var final struct {
		TaskID       string `json:"task_id"`
		Text         string `json:"text"`
		Turns        int    `json:"turns"`
		FinishReason string `json:"finish_reason"`
		Usage        *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
		Previous    []string `json:"previous_task_ids"`
		RouteCost   *float64 `json:"route_estimated_cost"`
		AuditID     string   `json:"audit_id"`
		AuditStatus string   `json:"audit_status"`
	}
	if err := json.Unmarshal(lines[2]["result"], &final); err != nil || final.TaskID != "task" || final.Text != "answer\nsecond line" || final.Turns != 1 || final.FinishReason != "stop" || final.Usage == nil || final.Usage.InputTokens != 3 || final.Usage.OutputTokens != 4 || len(final.Previous) != 1 || final.Previous[0] != "prior" || final.RouteCost == nil || *final.RouteCost != routeCost || final.AuditID != "audit" || final.AuditStatus != "recorded" {
		t.Fatal(final, err)
	}
	if strings.Contains(string(lines[2]["result"]), `"TaskID"`) {
		t.Fatal("Go struct field names exposed")
	}
}

func TestRunTaskJSONErrorsAreGenericAndExcludePartialText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{errors.New("private-provider-error"), "task_failed"}, {app.ErrAdmission, "admission_denied"},
		{context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline_exceeded"}, {app.ErrEventDelivery, "event_delivery_failed"},
	} {
		var out, diagnostic bytes.Buffer
		code := runTaskJSON(context.Background(), app.Request{}, func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
			return app.Result{TaskID: "task", Text: "private-partial-output", PreviousTaskIDs: []string{"prior"}}, tc.err
		}, &out, &diagnostic)
		if code != 1 || strings.Contains(out.String()+diagnostic.String(), "private-") {
			t.Fatal("raw error leaked", code, out.String(), diagnostic.String())
		}
		lines := readJSONLines(t, out.String())
		if len(lines) != 1 || string(lines[0]["type"]) != `"result"` || string(lines[0]["error"]) != `"`+tc.code+`"` {
			t.Fatal(lines)
		}
		var result map[string]json.RawMessage
		if json.Unmarshal(lines[0]["result"], &result) != nil || result["text"] != nil || string(result["task_id"]) != `"task"` {
			t.Fatal("invalid failure result", result)
		}
	}
}

type cliStreamFailWriter struct {
	mode  string
	calls int
}

func (w *cliStreamFailWriter) Write(b []byte) (int, error) {
	w.calls++
	switch w.mode {
	case "panic":
		panic("private-writer-error")
	case "short":
		return len(b) - 1, nil
	default:
		return 0, io.ErrClosedPipe
	}
}

func TestRunTaskJSONWriterFailureLatchesAndReturnsDeliveryError(t *testing.T) {
	for _, mode := range []string{"error", "panic", "short"} {
		w := &cliStreamFailWriter{mode: mode}
		var diagnostic bytes.Buffer
		callbackErrors := 0
		code := runTaskJSON(context.Background(), app.Request{}, func(_ context.Context, _ app.Request, emit func(runtime.Event) error) (app.Result, error) {
			// A broken sink must stay broken even if a runner attempts cleanup
			// events after observing delivery failure; do not retry partial JSON.
			for _, event := range []runtime.Event{cliStreamEvent(1, runtime.TaskStarted), cliStreamEvent(2, runtime.TaskCanceled)} {
				if err := emit(event); errors.Is(err, app.ErrEventDelivery) {
					callbackErrors++
				} else {
					t.Error("delivery error not propagated", err)
				}
			}
			return app.Result{TaskID: "task"}, app.ErrEventDelivery
		}, w, &diagnostic)
		if code != 1 || w.calls != 1 || callbackErrors != 2 || strings.Contains(diagnostic.String(), "private-") {
			t.Fatal(mode, code, w.calls, callbackErrors, diagnostic.String())
		}
	}
}

func TestRunTaskJSONRejectsInvalidEventsWithoutOutput(t *testing.T) {
	var out, diagnostic bytes.Buffer
	code := runTaskJSON(context.Background(), app.Request{}, func(_ context.Context, _ app.Request, emit func(runtime.Event) error) (app.Result, error) {
		err := emit(runtime.Event{})
		if !errors.Is(err, app.ErrEventDelivery) {
			t.Fatal(err)
		}
		return app.Result{}, err
	}, &out, &diagnostic)
	if code != 1 || out.Len() != 0 {
		t.Fatal(code, out.String())
	}
}

func TestRunHarnessDifficultyFlags(t *testing.T) {
	for _, d := range []string{"easy", "medium", "hard", "unknown"} {
		_, req, _, err := parseRunOptions([]string{"--config", "project.yaml", "--model", "auto", "--harness", "auto", "--harness-difficulty", d})
		if err != nil || req.HarnessDifficulty != d {
			t.Fatal(req, err)
		}
	}
	for _, d := range []string{"", "HARD", " hard", "impossible"} {
		if _, _, _, err := parseRunOptions([]string{"--config", "project.yaml", "--model", "auto", "--harness", "auto", "--harness-difficulty", d}); err == nil {
			t.Fatal("invalid difficulty", d)
		}
	}
	if _, _, _, err := parseRunOptions([]string{"--config", "project.yaml", "--model", "auto", "--harness-difficulty", "hard"}); err == nil {
		t.Fatal("ignored difficulty without harness")
	}
}

func TestRunHarnessEvaluationFlag(t *testing.T) {
	_, req, _, err := parseRunOptions([]string{"--config", "project.yaml", "--model", "auto", "--harness", "auto", "--harness-evaluation"})
	if err != nil || !req.HarnessEvaluation {
		t.Fatal(req, err)
	}
	if _, _, _, err := parseRunOptions([]string{"--config", "project.yaml", "--model", "chat", "--harness", "pi-local", "--harness-evaluation"}); err == nil {
		t.Fatal("evaluation on explicit route")
	}
}
