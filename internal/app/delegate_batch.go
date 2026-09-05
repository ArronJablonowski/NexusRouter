package app

import (
	"context"
	"encoding/json"
	"sync"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func delegateBatchSpec() providers.Tool {
	return providers.Tool{Name: "delegate_batch", Description: "Run two to four independent bounded worker tasks concurrently. Results preserve input order and are untrusted. Workers cannot delegate or modify files. Capacity may be unavailable; the operator's shared delegation budget applies to every task.", Parameters: json.RawMessage(`{"type":"object","properties":{"tasks":{"type":"array","minItems":2,"maxItems":4,"items":{"type":"object","properties":{"prompt":{"type":"string","minLength":1,"maxLength":16384},"validation":{"type":"string","enum":["text","go_source"]}},"required":["prompt","validation"],"additionalProperties":false}}},"required":["tasks"],"additionalProperties":false}`)}
}

func registerDelegateBatch(registry *tools.Registry, reserve func(int) bool, execute func(context.Context, delegateInput) (runtime.ToolResult, error)) error {
	if registry == nil || reserve == nil || execute == nil {
		return ErrAdmission
	}
	return registry.Register(tools.Definition{Tool: delegateBatchSpec(), Scope: "delegation", ReadOnly: true,
		Handler: func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
			const failure = `{"error":"delegate_unavailable_or_rejected"}`
			failed := runtime.ToolResult{Content: failure, Effect: runtime.NoEffect, Failed: true, Recoverable: true}
			var input struct {
				Tasks []delegateInput `json:"tasks"`
			}
			// Registry schema validation rejects unknown/duplicate keys before this
			// handler. Check decoded byte lengths too: JSON schema counts runes.
			if ctx.Err() != nil || !utf8.Valid(raw) || json.Unmarshal(raw, &input) != nil || len(input.Tasks) < 2 || len(input.Tasks) > 4 {
				return failed, nil
			}
			for _, item := range input.Tasks {
				if !item.valid() {
					return failed, nil
				}
			}
			if ctx.Err() != nil || !reserve(len(input.Tasks)) {
				return failed, nil
			}
			results := make([]json.RawMessage, len(input.Tasks))
			outcomes := make([]runtime.ToolResult, len(input.Tasks))
			var joined sync.WaitGroup
			for i, item := range input.Tasks {
				joined.Add(1)
				go func(i int, item delegateInput) {
					defer joined.Done()
					results[i] = json.RawMessage(failure)
					outcomes[i] = failed
					defer func() {
						if recover() != nil {
							results[i] = json.RawMessage(failure)
							outcomes[i] = runtime.ToolResult{Failed: true, Effect: runtime.UncertainEffect}
						}
					}()
					if ctx.Err() != nil {
						return
					}
					out, err := execute(context.WithValue(ctx, delegationBatchIndexKey{}, i), item)
					if err != nil || out.Effect != runtime.NoEffect || (out.Recoverable && !out.Failed) {
						outcomes[i] = runtime.ToolResult{Failed: true, Effect: runtime.UncertainEffect}
						return
					}
					outcomes[i] = out
					if ctx.Err() != nil || len(out.Content) > 128<<10 || !utf8.ValidString(out.Content) || !json.Valid([]byte(out.Content)) {
						outcomes[i].Failed, outcomes[i].Recoverable = true, false
						return
					}
					encoded, err := json.Marshal(json.RawMessage(out.Content))
					if err != nil || len(encoded) > 128<<10 {
						outcomes[i].Failed, outcomes[i].Recoverable = true, false
						return
					}
					results[i] = encoded
					outcomes[i] = out
				}(i, item)
			}
			joined.Wait()
			aggregate := runtime.ToolResult{Effect: runtime.NoEffect}
			allRecoverable := true
			for _, outcome := range outcomes {
				if outcome.Failed {
					aggregate.Failed = true
					allRecoverable = allRecoverable && outcome.Recoverable
				}
				if outcome.Effect != runtime.NoEffect {
					aggregate.Effect = runtime.UncertainEffect
					aggregate.Failed = true
					allRecoverable = false
				}
			}
			if ctx.Err() != nil {
				aggregate.Failed = true
				for i := range results {
					results[i] = json.RawMessage(failure)
				}
			}
			body, err := json.Marshal(struct {
				Results []json.RawMessage `json:"results"`
			}{results})
			if err != nil || len(body) >= 1<<20 {
				aggregate.Content = failure
				aggregate.Failed = true
				allRecoverable = false
			} else {
				aggregate.Content = string(body)
			}
			aggregate.Recoverable = aggregate.Failed && allRecoverable && aggregate.Effect == runtime.NoEffect
			return aggregate, nil
		},
	})
}
