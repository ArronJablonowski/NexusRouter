package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

const AgentAdapterVersion = "hermes-host-tools-v1"
const agentSystemPrompt = "Complete the task accurately using only the supplied host tools when needed, then return the requested answer."

type AgentConfig struct {
	Config
	Tools    []providers.Tool
	MaxTurns int
}
type agentExecution struct {
	config   AgentConfig
	identity harness.Identity
	session  *runtime.HarnessAgentSession
}

// Identity separates native tool evidence from the legacy text adapter and binds
// the bridge implementation, tool schemas, turn ceiling and host configuration.
func (c AgentConfig) Identity() (harness.Identity, error) {
	id, e := c.Config.Identity()
	if e != nil || c.MaxTurns < 1 || c.MaxTurns > 64 {
		return harness.Identity{}, ErrProjection
	}
	if validateAgentTools(c.Tools) != nil {
		return harness.Identity{}, ErrProjection
	}
	extension := sha256.Sum256([]byte(agentBridgeSource))
	body, e := json.Marshal(struct {
		Base, Prompt, Extension string
		Tools                   []providers.Tool
		Turns                   int
	}{id.ConfigSHA256, agentSystemPrompt, hex.EncodeToString(extension[:]), c.Tools, c.MaxTurns})
	if e != nil {
		return harness.Identity{}, ErrProjection
	}
	digest := sha256.Sum256(body)
	id.AdapterVersion = AgentAdapterVersion
	id.ConfigSHA256 = hex.EncodeToString(digest[:])
	return id, nil
}

// RunAgentTask is a host embedding entry point, not independent tool authority.
// Supply the normal submission-fenced/redacting journal, policy transport,
// resource admission and scoped approval/schema-enforcing executor. The returned
// execution is pending quality review. Application/SDK routing integration is
// separate; embedders may use RunAgent with their own managed lifecycle.
func RunAgentTask(ctx context.Context, j runtime.Journal, c AgentConfig, t Task, tools runtime.ToolExecutor) (TaskResult, error) {
	c, identity, e := prepareAgent(c, t.Prompt)
	if e != nil {
		return TaskResult{}, e
	}
	var native Result
	outcome, text, e := runtime.RunHarnessAgent(ctx, j, runtime.HarnessAgentRequest{
		Request:  runtime.HarnessRequest{TaskID: t.ID, SessionID: t.SessionID, SubmissionID: t.SubmissionID, Attribution: runtime.HarnessAttribution{Identity: identity, Task: t.Class}, Messages: c.Messages, ContextTokens: c.ContextTokens, MaxOutputBytes: t.MaxOutputBytes, OutputView: t.OutputView},
		MaxTurns: c.MaxTurns, Tools: tools,
		Execute: func(run context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
			var err error
			native, err = RunAgent(run, c, t.Prompt, s)
			return runtime.HarnessOutput{Actual: native.Identity, Text: native.Text}, err
		},
	})
	if e != nil {
		return TaskResult{Execution: outcome}, e
	}
	native.Text = text
	return TaskResult{Execution: outcome, Result: native}, nil
}

// RunAgent executes within an existing host-owned RunHarnessAgent session. The
// caller owns the journal lifecycle and may validate the returned response before
// committing success. Supply the same identity, tool policy and turn limit to the
// session as this configuration. This function does not create another task or
// commit a terminal event, and never supplies independent tool authority.
func RunAgent(ctx context.Context, c AgentConfig, prompt string, session *runtime.HarnessAgentSession) (Result, error) {
	if session == nil {
		return Result{}, ErrProjection
	}
	c, identity, err := prepareAgent(c, prompt)
	if err != nil {
		return Result{}, err
	}
	return runConfigured(ctx, c.Config, prompt, &agentExecution{c, identity, session})
}

func prepareAgent(c AgentConfig, prompt string) (AgentConfig, harness.Identity, error) {
	// Snapshot mutable host configuration before attribution or child startup.
	if c.Prices != nil {
		prices := *c.Prices
		c.Prices = &prices
	}
	body, e := json.Marshal(c.Tools)
	if e != nil {
		return AgentConfig{}, harness.Identity{}, ErrProjection
	}
	c.Tools = nil
	if json.Unmarshal(body, &c.Tools) != nil {
		return AgentConfig{}, harness.Identity{}, ErrProjection
	}
	body, e = json.Marshal(c.Messages)
	if e != nil {
		return AgentConfig{}, harness.Identity{}, ErrProjection
	}
	c.Messages = nil
	if json.Unmarshal(body, &c.Messages) != nil {
		return AgentConfig{}, harness.Identity{}, ErrProjection
	}
	identity, e := c.Identity()
	if e != nil {
		return AgentConfig{}, harness.Identity{}, e
	}
	if len(c.Messages) == 0 {
		c.Messages = []providers.Message{{Role: "system", Content: agentSystemPrompt}, {Role: "user", Content: prompt}}
	}
	return c, identity, nil
}
