package goose

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type Task struct {
	ID, SessionID, SubmissionID, Prompt string
	Class                               harness.TaskClass
	MaxOutputBytes                      int
	OutputView                          func(string) string
}
type TaskResult struct {
	Execution harness.Execution
	Result    Result
}

// RunTask executes through NexusRouter's durable task journal. The host must
// supply its normal submission-fenced/redacting journal and admission callback.
// Reusing an existing task ID fails before native execution; ambiguous terminal
// writes require inspecting the same journal, never rerunning inference. A
// committed execution is still pending quality review. This is an embedding
// entry point, not an independently authorized public SDK routing endpoint.
func RunTask(ctx context.Context, j runtime.Journal, c Config, t Task) (TaskResult, error) {
	if c.Prices != nil {
		prices := *c.Prices
		c.Prices = &prices
	}
	c.Messages = append([]providers.Message(nil), c.Messages...)
	if len(c.Messages) == 0 {
		c.Messages = []providers.Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: t.Prompt}}
	}
	identity, err := c.Identity()
	if err != nil {
		return TaskResult{}, err
	}
	var native Result
	outcome, text, err := runtime.RunHarness(ctx, j, runtime.HarnessRequest{
		Messages: c.Messages, TaskID: t.ID, SessionID: t.SessionID, SubmissionID: t.SubmissionID, Attribution: runtime.HarnessAttribution{Identity: identity, Task: t.Class}, ContextTokens: c.ContextTokens, MaxOutputBytes: t.MaxOutputBytes, OutputView: t.OutputView,
		Execute: func(run context.Context) (runtime.HarnessOutput, error) {
			var e error
			native, e = Run(run, c, t.Prompt)
			return runtime.HarnessOutput{Actual: native.Identity, Text: native.Text}, e
		},
	})
	if err != nil {
		return TaskResult{Execution: outcome}, err
	}
	native.Text = text
	return TaskResult{Execution: outcome, Result: native}, nil
}
