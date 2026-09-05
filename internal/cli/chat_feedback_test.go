package cli

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"darwinrouter/evaluation"
	"darwinrouter/routing"
)

func TestChatFeedbackExplicitChoices(t *testing.T) {
	calls := 0
	hooks := chatHooks{Feedback: func(ctx context.Context, task string, accepted bool, cost float64) error {
		calls++
		if task != "task" || !accepted || cost != 0 {
			t.Fatal(task, accepted, cost)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("unbounded hook")
		}
		return nil
	}, ReviseFeedback: func(ctx context.Context, task, expected string, accepted bool) error {
		calls++
		if task != "task" || expected != "record_1" || accepted {
			t.Fatal(task, expected, accepted)
		}
		return nil
	}}
	if out := runChatFeedback(context.Background(), "/feedback", "accepted 0", "task", hooks); out != "Feedback recorded.\n" {
		t.Fatal(out)
	}
	if out := runChatFeedback(context.Background(), "/feedback-revise", "record_1 rejected", "task", hooks); out != "Feedback revised.\n" {
		t.Fatal(out)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestChatFeedbackStrictParsingAndErrors(t *testing.T) {
	hooks := chatHooks{Feedback: func(context.Context, string, bool, float64) error { t.Fatal("invalid mutation"); return nil }, ReviseFeedback: func(context.Context, string, string, bool) error { t.Fatal("invalid revision"); return nil }}
	for _, input := range []string{"accepted", "yes 0", "accepted NaN", "accepted Inf", "accepted -1", "accepted 1 extra", "accepted privatecost"} {
		out := runChatFeedback(context.Background(), "/feedback", input, "task", hooks)
		if !strings.HasPrefix(out, "Usage:") || strings.Contains(out, "privatecost") {
			t.Fatal(out)
		}
	}
	for _, input := range []string{"record accepted extra", "record maybe", "../private accepted", strings.Repeat("a", 129) + " accepted"} {
		if out := runChatFeedback(context.Background(), "/feedback-revise", input, "task", hooks); !strings.HasPrefix(out, "Usage:") {
			t.Fatal(out)
		}
	}
	if out := runChatFeedback(context.Background(), "/feedback", "accepted 0", "", hooks); !strings.HasPrefix(out, "No completed task") {
		t.Fatal(out)
	}
	hooks.Feedback = func(context.Context, string, bool, float64) error { return errors.New("private hook error") }
	hooks.ReviseFeedback = func(context.Context, string, string, bool) error { return errors.New("stale private error") }
	for command, arg := range map[string]string{"/feedback": "accepted 0", "/feedback-revise": "record rejected"} {
		if out := runChatFeedback(context.Background(), command, arg, "task", hooks); out != "Feedback operation failed.\n" {
			t.Fatal(out)
		}
	}
}

func TestChatFeedbackHistoryOnlySafeMetadata(t *testing.T) {
	record := evaluation.Record{Version: 1, ID: "record_1", TaskID: "task", AttemptID: "privateattempt", Key: routing.Key{Model: "privatemodel", Provider: "privateprovider", Domain: "private", Profile: "private"}, Time: time.Now(), Cost: 123, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "private reference", Passed: true}}}
	hooks := chatHooks{FeedbackHistory: func(context.Context, string) ([]evaluation.Record, error) { return []evaluation.Record{record}, nil }}
	if out := runChatFeedback(context.Background(), "/feedback-show", "", "task", hooks); out != "Feedback history:\nrecord_1 accepted source=user_feedback\n" {
		t.Fatal(out)
	}
	record.Checks[0].Passed = false
	if out := runChatFeedback(context.Background(), "/feedback-show", "", "task", hooks); out != "Feedback history:\nrecord_1 rejected source=user_feedback\n" {
		t.Fatal(out)
	}
	for _, source := range []evaluation.Source{evaluation.Deterministic, evaluation.ToolResult, evaluation.LLMJudge} {
		record.Checks[0].Source = source
		record.AllowJudge = true
		if out := runChatFeedback(context.Background(), "/feedback-show", "", "task", hooks); out != "Feedback history:\nrecord_1 rejected source="+string(source)+"\n" {
			t.Fatal(out)
		}
	}
	record.Checks[0].Source = evaluation.Source("private arbitrary source")
	if out := runChatFeedback(context.Background(), "/feedback-show", "", "task", hooks); out != "Feedback operation failed.\n" {
		t.Fatal("invalidsource leaked", out)
	}
	record.Checks[0].Source = evaluation.UserFeedback
	record.ID = "bad\x1b[31m"
	if out := runChatFeedback(context.Background(), "/feedback-show", "", "task", hooks); out != "Feedback operation failed.\n" {
		t.Fatal("unsafe ID", out)
	}
	if out := runChatFeedback(context.Background(), "/feedback-show", "unexpected", "task", hooks); out != "Usage: /feedback-show\n" {
		t.Fatal(out)
	}
}

func TestChatFeedbackHistoryNotYetRecorded(t *testing.T) {
	hooks := chatHooks{FeedbackHistory: func(context.Context, string) ([]evaluation.Record, error) {
		return nil, errors.Join(errors.New("private detail"), sql.ErrNoRows)
	}}
	if out := runChatFeedback(context.Background(), "/feedback-show", "", "task", hooks); out != "No feedback history.\n" {
		t.Fatal(out)
	}
}
