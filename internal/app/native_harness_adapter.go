package app

import (
	"context"
	"net/http"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/goose"
	"github.com/ArronJablonowski/NexusRouter/harness/hermes"
	"github.com/ArronJablonowski/NexusRouter/harness/openclaw"
	"github.com/ArronJablonowski/NexusRouter/harness/openhands"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// Both identity selection and execution use this closed adapter registry. An
// unknown kind never falls back to Pi or to a direct provider completion.
type nativeAdapter struct {
	Identity        func() (harness.Identity, error)
	Run             func(context.Context, string) (runtime.HarnessOutput, error)
	MaxOutputTokens int
	Agent           *nativeAgentAdapter
}

// One host lifecycle is shared by all explicitly enabled native tool adapters.
type nativeAgentAdapter struct {
	Tools                     []providers.Tool
	MaxTurns, MaxOutputTokens int
	Run                       func(context.Context, string, *runtime.HarnessAgentSession) (runtime.HarnessOutput, error)
}

func nativeConfig(entry NativeHarness, p config.Provider, m config.Model, tokens int, policyDigest, key string, tr http.RoundTripper, messages []providers.Message, toolConfig nativeToolContract) (nativeAdapter, error) {
	if entry.Prices == nil {
		return nativeAdapter{}, ErrHarnessUnsupported
	}
	c := nativePiConfig(entry, p, m, tokens, policyDigest, key, tr, messages)
	if entry.NativeTools {
		if (entry.Kind != "pi" && entry.Kind != "openhands" && entry.Kind != "goose" && entry.Kind != "hermes" && entry.Kind != "openclaw") || len(toolConfig.Catalog) == 0 {
			return nativeAdapter{}, ErrHarnessUnsupported
		}
		c.TransportPolicySHA256 = toolConfig.policyDigest(policyDigest)
	}
	switch entry.Kind {
	case "pi":
		if entry.NativeTools {
			agent := pi.AgentConfig{Config: c, Tools: toolConfig.Catalog, MaxTurns: toolConfig.MaxTurns}
			return nativeAdapter{Identity: agent.Identity, MaxOutputTokens: c.MaxOutputTokens, Agent: &nativeAgentAdapter{Tools: agent.Tools, MaxTurns: agent.MaxTurns, MaxOutputTokens: agent.MaxOutputTokens, Run: func(ctx context.Context, prompt string, session *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				result, e := pi.RunAgent(ctx, agent, prompt, session)
				return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text}, e
			}}}, nil
		}
		return nativeAdapter{Identity: c.Identity, MaxOutputTokens: c.MaxOutputTokens, Run: func(ctx context.Context, prompt string) (runtime.HarnessOutput, error) {
			result, err := pi.Run(ctx, c, prompt)
			return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text, Usage: result.MeasuredUsage}, err
		}}, nil
	case "openclaw":
		oc := openclaw.Config{Executable: c.Executable, ExecutableSHA256: c.ExecutableSHA256, Provider: c.Provider, Model: c.Model, ModelRevision: c.ModelRevision, BaseURL: c.BaseURL, APIKey: c.APIKey, UpstreamProtocol: c.UpstreamProtocol, TransportPolicySHA256: c.TransportPolicySHA256, ContextTokens: c.ContextTokens, MaxOutputTokens: c.MaxOutputTokens, Timeout: c.Timeout, Messages: c.Messages, Transport: c.Transport, Admit: c.Admit, Prices: &openclaw.Prices{Input: c.Prices.Input, Output: c.Prices.Output, CacheRead: c.Prices.CacheRead, CacheWrite: c.Prices.CacheWrite}}
		if entry.NativeTools {
			agent := openclaw.AgentConfig{Config: oc, Tools: toolConfig.Catalog, MaxTurns: toolConfig.MaxTurns}
			return nativeAdapter{Identity: agent.Identity, MaxOutputTokens: oc.MaxOutputTokens, Agent: &nativeAgentAdapter{Tools: agent.Tools, MaxTurns: agent.MaxTurns, MaxOutputTokens: agent.MaxOutputTokens, Run: func(ctx context.Context, prompt string, session *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				result, e := openclaw.RunAgent(ctx, agent, prompt, session)
				return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text}, e
			}}}, nil
		}
		return nativeAdapter{Identity: oc.Identity, MaxOutputTokens: oc.MaxOutputTokens, Run: func(ctx context.Context, prompt string) (runtime.HarnessOutput, error) {
			result, err := openclaw.Run(ctx, oc, prompt)
			return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text, Usage: result.Usage}, err
		}}, nil
	case "goose":
		oc := goose.Config{Executable: c.Executable, ExecutableSHA256: c.ExecutableSHA256, Provider: c.Provider, Model: c.Model, ModelRevision: c.ModelRevision, BaseURL: c.BaseURL, APIKey: c.APIKey, UpstreamProtocol: c.UpstreamProtocol, TransportPolicySHA256: c.TransportPolicySHA256, ContextTokens: c.ContextTokens, MaxOutputTokens: c.MaxOutputTokens, Timeout: c.Timeout, Messages: c.Messages, Transport: c.Transport, Admit: c.Admit, Prices: &goose.Prices{Input: c.Prices.Input, Output: c.Prices.Output, CacheRead: c.Prices.CacheRead, CacheWrite: c.Prices.CacheWrite}}
		if entry.NativeTools {
			agent := goose.AgentConfig{Config: oc, Tools: toolConfig.Catalog, MaxTurns: toolConfig.MaxTurns}
			return nativeAdapter{Identity: agent.Identity, MaxOutputTokens: oc.MaxOutputTokens, Agent: &nativeAgentAdapter{Tools: agent.Tools, MaxTurns: agent.MaxTurns, MaxOutputTokens: agent.MaxOutputTokens, Run: func(ctx context.Context, prompt string, session *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				result, e := goose.RunAgent(ctx, agent, prompt, session)
				return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text}, e
			}}}, nil
		}
		return nativeAdapter{Identity: oc.Identity, MaxOutputTokens: oc.MaxOutputTokens, Run: func(ctx context.Context, prompt string) (runtime.HarnessOutput, error) {
			result, err := goose.Run(ctx, oc, prompt)
			return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text, Usage: result.Usage}, err
		}}, nil
	case "openhands":
		oc := openhands.Config{RuntimeSHA256: entry.RuntimeSHA256, Executable: c.Executable, ExecutableSHA256: c.ExecutableSHA256, Provider: c.Provider, Model: c.Model, ModelRevision: c.ModelRevision, BaseURL: c.BaseURL, APIKey: c.APIKey, UpstreamProtocol: c.UpstreamProtocol, TransportPolicySHA256: c.TransportPolicySHA256, ContextTokens: c.ContextTokens, MaxOutputTokens: c.MaxOutputTokens, Timeout: c.Timeout, Messages: c.Messages, Transport: c.Transport, Admit: c.Admit, Prices: &openhands.Prices{Input: c.Prices.Input, Output: c.Prices.Output, CacheRead: c.Prices.CacheRead, CacheWrite: c.Prices.CacheWrite}}
		if entry.NativeTools {
			agent := openhands.AgentConfig{Config: oc, Tools: toolConfig.Catalog, MaxTurns: toolConfig.MaxTurns}
			return nativeAdapter{Identity: agent.Identity, MaxOutputTokens: oc.MaxOutputTokens, Agent: &nativeAgentAdapter{Tools: agent.Tools, MaxTurns: agent.MaxTurns, MaxOutputTokens: agent.MaxOutputTokens, Run: func(ctx context.Context, prompt string, session *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				result, e := openhands.RunAgent(ctx, agent, prompt, session)
				return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text}, e
			}}}, nil
		}

		return nativeAdapter{Identity: oc.Identity, MaxOutputTokens: oc.MaxOutputTokens, Run: func(ctx context.Context, prompt string) (runtime.HarnessOutput, error) {
			result, err := openhands.Run(ctx, oc, prompt)
			return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text, Usage: result.Usage}, err
		}}, nil
	case "hermes":
		oc := hermes.Config{SourceDir: entry.HermesSourceDir, RuntimeSHA256: entry.RuntimeSHA256, Executable: c.Executable, ExecutableSHA256: c.ExecutableSHA256, Provider: c.Provider, Model: c.Model, ModelRevision: c.ModelRevision, BaseURL: c.BaseURL, APIKey: c.APIKey, UpstreamProtocol: c.UpstreamProtocol, TransportPolicySHA256: c.TransportPolicySHA256, ContextTokens: c.ContextTokens, MaxOutputTokens: c.MaxOutputTokens, Timeout: c.Timeout, Messages: c.Messages, Transport: c.Transport, Admit: c.Admit, Prices: &hermes.Prices{Input: c.Prices.Input, Output: c.Prices.Output, CacheRead: c.Prices.CacheRead, CacheWrite: c.Prices.CacheWrite}}
		if entry.NativeTools {
			agent := hermes.AgentConfig{Config: oc, Tools: toolConfig.Catalog, MaxTurns: toolConfig.MaxTurns}
			return nativeAdapter{Identity: agent.Identity, MaxOutputTokens: oc.MaxOutputTokens, Agent: &nativeAgentAdapter{Tools: agent.Tools, MaxTurns: agent.MaxTurns, MaxOutputTokens: agent.MaxOutputTokens, Run: func(ctx context.Context, prompt string, session *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				result, e := hermes.RunAgent(ctx, agent, prompt, session)
				return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text}, e
			}}}, nil
		}
		return nativeAdapter{Identity: oc.Identity, MaxOutputTokens: oc.MaxOutputTokens, Run: func(ctx context.Context, prompt string) (runtime.HarnessOutput, error) {
			result, err := hermes.Run(ctx, oc, prompt)
			return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text, Usage: result.Usage}, err
		}}, nil
	default:
		return nativeAdapter{}, ErrHarnessUnsupported
	}
}
