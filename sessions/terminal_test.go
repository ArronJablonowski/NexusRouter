package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"darwinrouter/providers"
	"darwinrouter/runtime"
)

type terminalJournal []runtime.Event

func (j *terminalJournal) Append(_ context.Context, _ int64, e runtime.Event) error {
	*j = append(*j, e)
	return nil
}

type terminalTools struct{}

func (terminalTools) Execute(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
	return runtime.ToolResult{Content: "found", Effect: runtime.NoEffect}, nil
}

func terminalHistory(t *testing.T, mode string) []runtime.Event {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events terminalJournal
	turns := 0
	loop := runtime.Loop{Journal: &events, Tools: terminalTools{}, Provider: summaryProvider(func(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		turns++
		if mode == "failed" || mode == "canceled" {
			if err := emit(providers.Chunk{Text: "partial"}); err != nil {
				return err
			}
			if mode == "canceled" {
				cancel()
			}
			return errors.New("provider detail")
		}
		if mode == "tools" && turns == 1 {
			if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
				return err
			}
			return emit(providers.Chunk{Usage: &providers.Usage{InputTokens: 3, OutputTokens: 2}, Done: true, FinishReason: "tool_calls"})
		}
		text := "answer"
		if mode == "go" {
			text = "package answer\nfunc Answer() int { return 42 }"
		}
		return emit(providers.Chunk{Text: text, Usage: &providers.Usage{InputTokens: 5, OutputTokens: 7}, Done: true, FinishReason: "stop"})
	})}
	r := runtime.RunRequest{SubmissionID: "submission", TaskID: "task", SessionID: "task", ProviderID: "provider", RequireText: true, Domain: "code", Profile: "default", MaxTurns: 3, MaxOutputBytes: 4096, Inference: providers.Request{Model: "model", Messages: []providers.Message{{Role: "user", Content: "hello"}}}}
	if mode == "go" {
		r.Validation = "go_source"
	}
	_, err := loop.Run(ctx, r)
	if mode != "failed" && mode != "canceled" && err != nil {
		t.Fatal(err)
	}
	return events
}

func TestProjectTerminalRuntimeHistories(t *testing.T) {
	for _, mode := range []string{"success", "tools", "go", "failed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			out, err := ProjectTerminalSubmission(terminalHistory(t, mode))
			if err != nil || out.Result == nil || out.Result.TaskID != "task" || out.Result.AuditStatus != "not_recovered" {
				t.Fatal(out, err)
			}
			if mode == "failed" || mode == "canceled" {
				if out.State != mode || out.Result.Text != "" || out.Result.Usage != nil {
					t.Fatal(out)
				}
			} else {
				if out.State != "succeeded" || out.Result.Text == "" || out.Result.Usage == nil {
					t.Fatal(out)
				}
				if mode == "tools" && (out.Result.Turns != 2 || out.Result.Usage.InputTokens != 8 || out.Result.Usage.OutputTokens != 9) {
					t.Fatal(out.Result)
				}
			}
		})
	}
}

func TestProjectTerminalRejectsUnprovenSuccess(t *testing.T) {
	for _, mode := range []string{"missingcheck", "negative", "foreignmodel", "foreignturn", "foreignprovider", "foreigndomain", "foreignprofile", "foreignattempt", "binding", "blank", "running", "duplicate", "missinggo", "badgo", "unknownvalidation"} {
		t.Run(mode, func(t *testing.T) {
			kind := "success"
			if mode == "missinggo" || mode == "badgo" {
				kind = "go"
			}
			events := terminalHistory(t, kind)
			check, end := 0, 0
			for i, e := range events {
				if e.Kind == runtime.EvaluationRecorded {
					check = i
				}
				if e.Kind == runtime.TurnCompleted {
					end = i
				}
			}
			switch mode {
			case "missingcheck", "missinggo":
				events[check].Data.Code = "other.check"
			case "negative":
				no := false
				events[check].Data.Accepted = &no
			case "foreignmodel":
				events[check].Data.ModelID = "foreign"
			case "foreignturn":
				events[check].Data.ModelID = "foreign"
				for i := range events {
					if events[i].Kind == runtime.TurnStarted {
						events[i].Data.ModelID = "foreign"
					}
				}
			case "foreignprovider":
				events[check].Data.ProviderID = "foreign"
			case "foreigndomain":
				events[check].Data.Domain = "foreign"
			case "foreignprofile":
				events[check].Data.Profile = "foreign"
			case "foreignattempt":
				events[check].AttemptID = "foreign"
			case "binding":
				events[0].Data.SubmissionID = ""
			case "blank":
				events[end].Data.Text = " \n"
			case "running":
				events = events[:len(events)-1]
			case "duplicate":
				duplicate := events[check]
				duplicate.ID = "duplicate"
				events = append(events[:check+1], append([]runtime.Event{duplicate}, events[check+1:]...)...)
				for i := range events {
					events[i].Sequence = int64(i + 1)
				}
			case "badgo":
				events[end].Data.Text = "not Go source"
			case "unknownvalidation":
				events[0].Data.Validation = "unknown"
			}
			if _, err := ProjectTerminalSubmission(events); !errors.Is(err, ErrHistory) {
				t.Fatal("unproven success accepted", err)
			}
		})
	}
}

func TestProjectTerminalUsageUnknownAndOverflow(t *testing.T) {
	for _, mode := range []string{"missing", "negative", "overflow"} {
		events := terminalHistory(t, "tools")
		for i := range events {
			if events[i].Kind == runtime.TurnCompleted {
				switch mode {
				case "missing":
					events[i].Data.Usage = nil
				case "negative":
					events[i].Data.Usage = &providers.Usage{InputTokens: -1}
				case "overflow":
					events[i].Data.Usage = &providers.Usage{InputTokens: math.MaxInt64}
				}
			}
		}
		out, err := ProjectTerminalSubmission(events)
		if err != nil || out.Result.Usage != nil {
			t.Fatal(mode, out, err)
		}
	}
}

func TestProjectTerminalBounds(t *testing.T) {
	if _, err := ProjectTerminalSubmission(make([]runtime.Event, 10001)); !errors.Is(err, ErrHistory) {
		t.Fatal(err)
	}
	events := terminalHistory(t, "success")
	events[0].Data.Messages[0].Content = strings.Repeat("x", 8<<20)
	if _, err := ProjectTerminalSubmission(events); !errors.Is(err, ErrHistory) {
		t.Fatal(err)
	}
}

func TestProjectTerminalFailureBeforeAnyTurn(t *testing.T) {
	events := terminalHistory(t, "failed")
	events = []runtime.Event{events[0], events[len(events)-1]}
	events[1].Sequence = 2
	events[1].TurnID, events[1].AttemptID = "", ""
	out, err := ProjectTerminalSubmission(events)
	if err != nil || out.State != "failed" || out.Result.Turns != 0 || out.Result.Usage != nil || out.Result.Text != "" {
		t.Fatal(out, err)
	}
}
