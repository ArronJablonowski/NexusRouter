package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestResponseContractRepairPersistsAndReplays(t *testing.T) {
	db, _ := store(t)
	calls := 0
	l := runtime.Loop{Journal: db, Provider: model(func(ctx context.Context, req providers.Request, emit func(providers.Chunk) error) error {
		calls++
		answer := `{"ok": true}`
		if calls == 2 {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "user" || !strings.Contains(strings.ToLower(last.Content), "json") {
				t.Fatalf("missing public correction: %+v", last)
			}
			history, err := db.Read(ctx, "task", 0, 100)
			if err != nil || history[len(history)-2].Kind != runtime.ResponseRevision {
				t.Fatalf("revision not durable before next provider: %+v %v", history, err)
			}
			answer = `{"ok":true}`
		}
		return emit(providers.Chunk{Text: answer, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 10, OutputTokens: 8}})
	})}
	r := runRequest()
	r.MaxTurns, r.RequireText, r.ResponseInstructions = 4, true, "Return only compact JSON."
	out, err := l.Run(context.Background(), r)
	if err != nil || calls != 2 || out.Text != `{"ok":true}` || out.Usage.OutputTokens != 16 {
		t.Fatalf("out=%+v calls=%d err=%v", out, calls, err)
	}
	snap, err := sessions.Replay(context.Background(), db, r.TaskID)
	if err != nil || snap.State != "completed" || len(snap.Messages) != 4 || snap.Messages[2].Role != "user" {
		t.Fatalf("replay=%+v err=%v", snap, err)
	}
	events, _ := db.Read(context.Background(), r.TaskID, 0, 100)
	// Projection fixture adds a submission binding; the direct runtime test
	// does not claim the telemetry dispatcher's separate execution lease.
	events[0].Data.SubmissionID = "submission"
	terminal, err := sessions.ProjectTerminalSubmission(events)
	if err != nil || terminal.Result.Text != out.Text {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
}

func TestResponseContractRepairBoundedPreservesFailedCandidate(t *testing.T) {
	db, _ := store(t)
	calls := 0
	l := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		calls++
		return emit(providers.Chunk{Text: `{"ok": true}`, Done: true, FinishReason: "stop"})
	})}
	r := runRequest()
	r.MaxTurns, r.RequireText, r.ResponseInstructions = 8, true, "Return only compact JSON."
	out, err := l.Run(context.Background(), r)
	if err != nil || calls != 3 || out.Text != `{"ok": true}` {
		t.Fatalf("out=%+v calls=%d err=%v", out, calls, err)
	}
	events, _ := db.Read(context.Background(), r.TaskID, 0, 100)
	revisions, failedChecks := 0, 0
	for _, e := range events {
		if e.Kind == runtime.ResponseRevision {
			revisions++
		}
		if e.Kind == runtime.EvaluationRecorded && e.Data.Code == "deterministic.response_contract.v1" && e.Data.Accepted != nil && !*e.Data.Accepted {
			failedChecks++
		}
	}
	if revisions != 2 || failedChecks != 1 {
		t.Fatalf("revisions=%d failedChecks=%d", revisions, failedChecks)
	}
}

func TestResponseContractRepairCannotEscapeOutputBudget(t *testing.T) {
	db, _ := store(t)
	calls := 0
	l := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		calls++
		return emit(providers.Chunk{Text: `{"ok": true}`, Done: true, FinishReason: "stop", Usage: &providers.Usage{OutputTokens: 8}})
	})}
	r := runRequest()
	r.MaxTurns, r.ResponseInstructions, r.Inference.MaxOutputTokens = 8, "Return only compact JSON.", 8
	out, err := l.Run(context.Background(), r)
	if err != nil || calls != 1 || out.Text != `{"ok": true}` {
		t.Fatalf("calls=%d out=%+v err=%v", calls, out, err)
	}
}

func TestResponseContractRepairCannotExecuteTools(t *testing.T) {
	db, _ := store(t)
	calls := 0
	l := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, req providers.Request, emit func(providers.Chunk) error) error {
		calls++
		if calls == 1 {
			return emit(providers.Chunk{Text: `{"ok": true}`, Done: true, FinishReason: "stop"})
		}
		if len(req.Tools) != 0 {
			t.Fatal("tools still advertised during format correction")
		}
		return emitCall(emit)
	}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		t.Fatal("format correction executed tool")
		return runtime.ToolResult{}, nil
	})}
	r := runRequest()
	r.MaxTurns, r.ResponseInstructions = 4, "Return only compact JSON."
	_, err := l.Run(context.Background(), r)
	if !errors.Is(err, runtime.ErrTool) {
		t.Fatalf("err=%v", err)
	}
}

func TestResponseContractSmallContextPreservesGradeableCandidate(t *testing.T) {
	db, _ := store(t)
	calls := 0
	l := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		calls++
		return emit(providers.Chunk{Text: `{"ok": true}`, Done: true, FinishReason: "stop"})
	})}
	r := runRequest()
	initialEstimate, estimateErr := providers.EstimateWith(context.Background(), nil, r.Inference)
	if estimateErr != nil {
		t.Fatal(estimateErr)
	}
	r.MaxTurns, r.MaxContextTokens, r.ResponseInstructions = 4, initialEstimate+8, "Return only compact JSON."
	out, err := l.Run(context.Background(), r)
	if err != nil || calls != 1 || out.Text != `{"ok": true}` {
		t.Fatalf("optional repair lost existing candidate: out=%+v calls=%d err=%v", out, calls, err)
	}
}
