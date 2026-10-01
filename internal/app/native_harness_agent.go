package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/responsecontract"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func runNativeAgentAdmitted(ctx context.Context, r Request, m config.Model, c *pi.AgentConfig, identity harness.Identity, task harness.TaskClass, tokens int, messages []providers.Message, privacy string, j runtime.Journal, executor runtime.ToolExecutor, result Result, sessionID string, secrets []string) (Result, error) {
	outcome, text, err := runtime.RunHarnessAgent(ctx, j, runtime.HarnessAgentRequest{
		Request:  runtime.HarnessRequest{TaskID: result.TaskID, SessionID: sessionID, SubmissionID: r.submissionID, Attribution: runtime.HarnessAttribution{Identity: identity, Task: task, Selection: r.nativeSelection}, ContextTokens: tokens, MaxOutputBytes: 1 << 20, Messages: messages, Privacy: privacy, OutputView: func(text string) string { return redact(text, secrets) }},
		MaxTurns: c.MaxTurns, Tools: executor,
		Execute: func(run context.Context, session *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
			estimate, e := providers.EstimateWith(run, r.contextEstimator, providers.Request{Model: m.Model, Messages: messages, Tools: c.Tools, ContextTokens: int64(tokens), MaxOutputTokens: int64(c.MaxOutputTokens)})
			if e != nil || estimate+c.MaxOutputTokens > tokens {
				return runtime.HarnessOutput{}, runtime.ErrContextOverflow
			}
			native, e := pi.RunAgent(run, *c, "Execute the host-supplied task context.", session)
			instructions := responseInstructions(r)
			if e == nil && redact(instructions, secrets) == instructions && len(responsecontract.Infer(instructions).Validate(redact(native.Text, secrets))) > 0 {
				e = runtime.ErrInvalidOutput
			}
			return runtime.HarnessOutput{Actual: native.Identity, Text: native.Text}, e
		},
	})
	result.Text = text
	result.HarnessSelection = r.nativeSelection
	result.HarnessOutcome = nil
	if err == nil {
		result.HarnessOutcome = &outcome
		result.FinishReason = "stop"
	}
	return result, err
}
