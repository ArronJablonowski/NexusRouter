package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestLiteralResponseCorrectionUsesStaticGuidanceAndSameTask(t *testing.T) {
	db, _ := store(t)
	calls := 0
	l := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, req providers.Request, emit func(providers.Chunk) error) error {
		calls++
		answer := "Summary\n\nFinal\n```\nREADY_482\n```"
		if calls == 2 {
			guidance := req.Messages[len(req.Messages)-1].Content
			if strings.Contains(guidance, "READY_482") || !strings.Contains(guidance, "literal") || len(req.Tools) != 0 {
				t.Fatalf("correction disclosed the target or retained tools: %q", guidance)
			}
			answer = "READY_482"
		}
		return emit(providers.Chunk{Text: answer, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 8, OutputTokens: 4}})
	})}
	r := runRequest()
	r.RequireText = true
	r.ResponseInstructions = "Request label. Reply with exactly: READY_482"
	r.Inference.Messages[0].Content = r.ResponseInstructions
	out, err := l.Run(context.Background(), r)
	if err != nil || calls != 2 || out.Text != "READY_482" || out.Usage.OutputTokens != 8 {
		t.Fatalf("literal correction did not complete within the original task: %+v calls=%d err=%v", out, calls, err)
	}
	events, err := db.Read(context.Background(), r.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	revisions, completions := 0, 0
	for _, event := range events {
		if event.Kind == runtime.ResponseRevision {
			revisions++
			if strings.Contains(event.Data.Text, "READY_482") {
				t.Fatal("diagnostic correction persisted the literal target")
			}
		}
		if event.Kind == runtime.TaskCompleted {
			completions++
		}
	}
	if revisions != 1 || completions != 1 {
		t.Fatalf("unexpected task/revision attribution: revisions=%d completions=%d", revisions, completions)
	}
}

func TestLiteralContainingConfiguredSecretCannotDemandItsRestoration(t *testing.T) {
	db, _ := store(t)
	const secret = "PRIVATE_CREDENTIAL_381"
	scrub := func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") }
	calls := 0
	l := runtime.Loop{Journal: db, ValidationText: scrub, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		calls++
		return emit(providers.Chunk{Text: "[REDACTED]", Done: true, FinishReason: "stop"})
	})}
	r := runRequest()
	r.RequireText = true
	r.ResponseInstructions = "Reply with exactly: " + secret
	r.Inference.Messages[0].Content = scrub(r.ResponseInstructions)
	out, err := l.Run(context.Background(), r)
	if err != nil || calls != 1 || out.Text != "[REDACTED]" {
		t.Fatalf("literal validation tried to defeat host redaction: %+v calls=%d err=%v", out, calls, err)
	}
	events, err := db.Read(context.Background(), r.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		body, err := event.Encode()
		if err != nil || strings.Contains(string(body), secret) || event.Kind == runtime.ResponseRevision || event.Data.Code == "deterministic.response_contract.v1" {
			t.Fatalf("private literal created impossible or leaking validation: kind=%s code=%s err=%v", event.Kind, event.Data.Code, err)
		}
	}
}

func TestLiteralInstructionProjectionFailureStopsBeforeProvider(t *testing.T) {
	for _, transform := range []func(string) string{
		func(string) string { panic("PRIVATE_PROJECTION_ERROR") },
		func(string) string { return strings.Repeat("x", 1<<20+1) },
	} {
		db, _ := store(t)
		l := runtime.Loop{Journal: db, ValidationText: transform, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
			t.Fatal("invalid host instruction projection reached provider")
			return nil
		})}
		r := runRequest()
		r.ResponseInstructions = "Reply with exactly: READY"
		_, err := l.Run(context.Background(), r)
		if !errors.Is(err, runtime.ErrInvalidRun) || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatalf("host projection failed unsafely: %v", err)
		}
	}
}

func TestLiteralValidationProjectionPreservesNonemptyAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transform func(string) string
		want      error
	}{
		{"empty", func(string) string { return "" }, runtime.ErrEmptyOutput},
		{"panic", func(string) string { panic("PRIVATE_PROJECTION_ERROR") }, runtime.ErrInvalidOutput},
		{"oversized", func(string) string { return strings.Repeat("x", 16<<20+1) }, runtime.ErrInvalidOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := store(t)
			l := runtime.Loop{Journal: db, ValidationText: func(value string) string {
				if strings.HasPrefix(value, "Reply with exactly:") {
					return value
				}
				return tc.transform(value)
			}, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				return emit(providers.Chunk{Text: "READY", Done: true, FinishReason: "stop"})
			})}
			r := runRequest()
			r.MaxTurns, r.RequireText, r.ResponseInstructions = 1, true, "Reply with exactly: READY"
			_, err := l.Run(context.Background(), r)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatalf("invalid transformed answer did not fail safely: %v", err)
			}
		})
	}
}
