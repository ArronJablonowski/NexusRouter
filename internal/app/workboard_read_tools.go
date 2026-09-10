package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/webui"
)

const maxWorkboardToolResultBytes = 1 << 20

func workboardListSpec() providers.Tool {
	return providers.Tool{Name: "workboard_list", Description: "List a bounded page of local DarwinRouter Kanban boards. Use the returned next_cursor to request another page.", Parameters: json.RawMessage(`{"type":"object","properties":{"after":{"type":"string","minLength":1,"maxLength":512},"limit":{"type":"integer","minimum":1,"maximum":100},"state":{"type":"string","enum":["active","archived"]}},"required":["limit"],"additionalProperties":false}`)}
}

func workboardReadSpec() providers.Tool {
	return providers.Tool{Name: "workboard_read", Description: "Read a bounded page of cards and lifecycle state from one local DarwinRouter Kanban board. This tool cannot mutate board state.", Parameters: json.RawMessage(`{"type":"object","properties":{"board_id":{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$"},"after":{"type":"string","minLength":1,"maxLength":512},"limit":{"type":"integer","minimum":1,"maximum":100},"state":{"type":"string","enum":["backlog","ready","in_progress","blocked","review","done","canceled"]},"assignee_id":{"type":"string","pattern":"^(unassigned|[A-Za-z0-9][A-Za-z0-9_-]{0,127})$"},"owner_id":{"type":"string","pattern":"^(unassigned|[A-Za-z0-9][A-Za-z0-9_-]{0,127})$"},"claim_state":{"type":"string","enum":["unclaimed","active","attention"]}},"required":["board_id","limit"],"additionalProperties":false}`)}
}

func registerWorkboardReadTools(registry *tools.Registry, store *telemetry.Store) error {
	if registry == nil || store == nil {
		return ErrAdmission
	}
	bridge, err := NewWorkboardBridge(store, store, defaultWorkboardNow)
	if err != nil {
		return err
	}
	failure := runtime.ToolResult{Content: `{"error":"workboard_unavailable"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}
	definitions := []tools.Definition{
		{Tool: workboardListSpec(), Scope: "workboards", ReadOnly: true, Behavior: runtime.BehaviorReadOnly, Handler: func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
			var options webui.BoardListOptions
			if json.Unmarshal(raw, &options) != nil || options.Validate() != nil || ctx.Err() != nil {
				return failure, nil
			}
			page, err := bridge.RootAgentList(ctx, options)
			return workboardToolResult(page, err, failure)
		}},
		{Tool: workboardReadSpec(), Scope: "workboards", ReadOnly: true, Behavior: runtime.BehaviorReadOnly, Handler: func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
			var args struct {
				BoardID string `json:"board_id"`
				webui.BoardSnapshotOptions
			}
			if json.Unmarshal(raw, &args) != nil || args.BoardSnapshotOptions.Validate() != nil || ctx.Err() != nil {
				return failure, nil
			}
			page, err := bridge.RootAgentRead(ctx, args.BoardID, args.BoardSnapshotOptions)
			return workboardToolResult(page, err, failure)
		}},
	}
	for _, definition := range definitions {
		if err := registry.Register(definition); err != nil {
			return err
		}
	}
	return nil
}

func defaultWorkboardNow() time.Time { return time.Now() }

func workboardToolResult(value any, err error, failure runtime.ToolResult) (runtime.ToolResult, error) {
	if err != nil {
		return failure, nil
	}
	body, err := json.Marshal(value)
	if err != nil || len(body) > maxWorkboardToolResultBytes {
		return failure, nil
	}
	return runtime.ToolResult{Content: string(body), Effect: runtime.NoEffect}, nil
}
