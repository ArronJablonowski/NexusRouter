package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

const (
	workboardIDSchema        = `{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$"}`
	workboardKeySchema       = `{"type":"string","minLength":16,"maxLength":128,"pattern":"^[!-~]+$"}`
	workboardLabelsSchema    = `{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":64}}`
	workboardIDsSchema       = `{"type":"array","maxItems":64,"uniqueItems":true,"items":` + workboardIDSchema + `}`
	workboardBudgetSchema    = `{"type":"object","properties":{"attempt_limit":{"type":"integer","minimum":1,"maximum":32},"time_limit_ms":{"type":"integer","minimum":0,"maximum":2592000000},"token_limit":{"type":"integer","minimum":0,"maximum":1000000000},"cost_micros":{"type":"integer","minimum":0,"maximum":1000000000000}},"required":["attempt_limit","time_limit_ms","token_limit","cost_micros"],"additionalProperties":false}`
	workboardCriterionSchema = `{"type":"object","properties":{"version":{"const":1},"id":` + workboardIDSchema + `,"kind":{"enum":["objective","subjective"]},"required_source":{"enum":["deterministic","user_feedback"]},"validator_id":` + workboardIDSchema + `,"description":{"type":"string","minLength":1,"maxLength":4096},"required":{"type":"boolean"}},"required":["version","id","kind","required_source","validator_id","description","required"],"additionalProperties":false,"allOf":[{"if":{"properties":{"kind":{"const":"objective"}}},"then":{"properties":{"required_source":{"const":"deterministic"}}}},{"if":{"properties":{"kind":{"const":"subjective"}}},"then":{"properties":{"required_source":{"const":"user_feedback"}}}}]}`
)

func workboardMutationSpecs() []providers.Tool {
	return []providers.Tool{
		{Name: "workboard_create_board", Description: "Create a local DarwinRouter Kanban board after operator approval.", Parameters: json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"title":{"type":"string","minLength":1,"maxLength":256},"description":{"type":"string","maxLength":65536}},"required":["idempotency_key","title"],"additionalProperties":false}`)},
		workboardBoardReviseMutationSpec(),
		workboardBoardArchiveMutationSpec(),
		{Name: "workboard_create_card", Description: "Create a bounded card with explicit acceptance criteria on one local Kanban board after operator approval.", Parameters: json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"title":{"type":"string","minLength":1,"maxLength":256},"description":{"type":"string","maxLength":65536},"priority":{"enum":["low","normal","high","urgent"]},"parent_id":` + workboardIDSchema + `,"assignee_id":` + workboardIDSchema + `,"labels":` + workboardLabelsSchema + `,"dependencies":` + workboardIDsSchema + `,"budget":` + workboardBudgetSchema + `,"criteria":{"type":"array","minItems":1,"maxItems":32,"items":` + workboardCriterionSchema + `},"expected_board_revision":{"type":"integer","minimum":1},"expected_graph_revision":{"type":"integer","minimum":1}},"required":["idempotency_key","board_id","title","criteria","expected_board_revision","expected_graph_revision"],"additionalProperties":false}`)},
		{Name: "workboard_update_card", Description: "Update bounded card fields using exact card and conditional graph revisions after operator approval.", Parameters: json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"card_id":` + workboardIDSchema + `,"title":{"type":"string","minLength":1,"maxLength":256},"description":{"type":"string","maxLength":65536},"priority":{"enum":["low","normal","high","urgent"]},"parent_id":` + workboardIDSchema + `,"assignee_id":` + workboardIDSchema + `,"clear_parent":{"const":true},"clear_assignee":{"const":true},"labels":` + workboardLabelsSchema + `,"budget":` + workboardBudgetSchema + `,"expected_card_revision":{"type":"integer","minimum":1},"expected_graph_revision":{"type":"integer","minimum":1}},"required":["idempotency_key","board_id","card_id","expected_card_revision"],"additionalProperties":false,"anyOf":[{"required":["title"]},{"required":["description"]},{"required":["priority"]},{"required":["parent_id"]},{"required":["assignee_id"]},{"required":["clear_parent"]},{"required":["clear_assignee"]},{"required":["labels"]},{"required":["budget"]}],"allOf":[{"not":{"required":["parent_id","clear_parent"]}},{"not":{"required":["assignee_id","clear_assignee"]}},{"if":{"anyOf":[{"required":["parent_id"]},{"required":["clear_parent"]}]},"then":{"required":["expected_graph_revision"]},"else":{"not":{"required":["expected_graph_revision"]}}}]}`)},
		{Name: "workboard_transition_card", Description: "Move a card to Backlog or Ready using exact board, layout, and card revisions after operator approval.", Parameters: json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"card_id":` + workboardIDSchema + `,"target_state":{"enum":["backlog","ready"]},"before_card_id":` + workboardIDSchema + `,"after_card_id":` + workboardIDSchema + `,"expected_board_revision":{"type":"integer","minimum":1},"expected_layout_revision":{"type":"integer","minimum":1},"expected_card_revision":{"type":"integer","minimum":1}},"required":["idempotency_key","board_id","card_id","target_state","expected_board_revision","expected_layout_revision","expected_card_revision"],"additionalProperties":false,"not":{"required":["before_card_id","after_card_id"]}}`)},
		workboardReorderMutationSpec(),
		{Name: "workboard_add_dependency", Description: "Add one same-board card dependency using exact card and graph revisions after operator approval.", Parameters: workboardDependencyMutationSchema()},
		{Name: "workboard_remove_dependency", Description: "Remove one same-board card dependency using exact card and graph revisions after operator approval.", Parameters: workboardDependencyMutationSchema()},
		workboardControlMutationSpec("workboard_request_pause"),
		workboardControlMutationSpec("workboard_request_resume"),
		workboardControlMutationSpec("workboard_request_cancel"),
	}
}

func workboardDependencyMutationSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"card_id":` + workboardIDSchema + `,"dependency_id":` + workboardIDSchema + `,"expected_card_revision":{"type":"integer","minimum":1},"expected_graph_revision":{"type":"integer","minimum":1}},"required":["idempotency_key","board_id","card_id","dependency_id","expected_card_revision","expected_graph_revision"],"additionalProperties":false}`)
}

func registerWorkboardMutationTools(registry *tools.Registry, bridge *WorkboardBridge) error {
	if registry == nil || bridge == nil {
		return ErrAdmission
	}
	boardScope, err := tools.IdentifierScope("workboard", "board_id")
	if err != nil {
		return ErrAdmission
	}
	for _, spec := range workboardMutationSpecs() {
		action, ok := workboardMutationAction(spec.Name)
		if !ok {
			return ErrAdmission
		}
		definition := tools.Definition{Tool: spec, Scope: "workboards", Behavior: runtime.BehaviorIdempotentWrite,
			Handler: workboardMutationHandler(bridge, action)}
		if action != webui.BoardCreate {
			definition.Scope = "workboard"
			definition.ResolveScope = boardScope
		}
		if err := registry.Register(definition); err != nil {
			return err
		}
	}
	return nil
}

func workboardMutationAction(name string) (webui.BoardAction, bool) {
	actions := map[string]webui.BoardAction{
		"workboard_create_board": webui.BoardCreate, "workboard_revise_board": webui.BoardRevise,
		"workboard_archive_board": webui.BoardArchive, "workboard_create_card": webui.CardCreate,
		"workboard_update_card": webui.CardRevise, "workboard_transition_card": webui.CardMove,
		"workboard_reorder_card":   webui.CardReorder,
		"workboard_add_dependency": webui.DependencyAdd, "workboard_remove_dependency": webui.DependencyRemove,
		"workboard_request_pause": webui.CardPauseRequest, "workboard_request_resume": webui.CardResumeRequest,
		"workboard_request_cancel": webui.CardCancelRequest,
	}
	action, ok := actions[name]
	return action, ok
}

func workboardMutationHandler(bridge *WorkboardBridge, action webui.BoardAction) func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
	return func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
		request := webui.BoardRequest{Version: webui.ContractVersion, Action: action}
		if json.Unmarshal(raw, &request) != nil || request.Validate() != nil || ctx.Err() != nil {
			return runtime.ToolResult{Content: `{"error":"workboard_mutation_invalid"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}, nil
		}
		receipt, err := bridge.RootAgentMutate(ctx, request)
		if err != nil {
			if violation := definitiveWorkboardNoEffect(err); violation != nil {
				body, marshalErr := json.Marshal(struct {
					Error string              `json:"error"`
					Code  workboard.ErrorCode `json:"code"`
				}{Error: "workboard_mutation_rejected", Code: violation.Code})
				if marshalErr == nil {
					return runtime.ToolResult{Content: string(body), Effect: runtime.NoEffect, Failed: true, Recoverable: true}, nil
				}
			}
			return runtime.ToolResult{Effect: runtime.UncertainEffect, Failed: true}, errors.New("workboard mutation acknowledgement uncertain")
		}
		body, err := json.Marshal(receipt)
		if err != nil || len(body) > maxWorkboardToolResultBytes {
			return runtime.ToolResult{Effect: runtime.UncertainEffect, Failed: true}, errors.New("workboard mutation receipt unavailable")
		}
		return runtime.ToolResult{Content: string(body), Effect: runtime.ConfirmedEffect}, nil
	}
}

// Only domain rejections proven to precede a committed mutation may invite a
// repaired model call. CodeInvalid is deliberately excluded: it can also
// describe a corrupt replay receipt discovered after an earlier commit.
func definitiveWorkboardNoEffect(err error) *workboard.Violation {
	var violation *workboard.Violation
	if !errors.As(err, &violation) {
		return nil
	}
	switch violation.Code {
	case workboard.CodeStaleRevision, workboard.CodeIllegalTransition, workboard.CodeMissingNode,
		workboard.CodeCrossBoard, workboard.CodeCycle, workboard.CodeDepthExhausted,
		workboard.CodeVisitsExhausted, workboard.CodeLimitExceeded:
		return violation
	default:
		return nil
	}
}
