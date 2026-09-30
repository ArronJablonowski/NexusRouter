package app

import (
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func workboardReorderMutationSpec() providers.Tool {
	return providers.Tool{
		Name:        "workboard_reorder_card",
		Description: "Reorder one card relative to exactly one same-lane neighbor using exact board, layout, and card revisions after operator approval.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"card_id":` + workboardIDSchema + `,"before_card_id":` + workboardIDSchema + `,"after_card_id":` + workboardIDSchema + `,"expected_board_revision":{"type":"integer","minimum":1},"expected_layout_revision":{"type":"integer","minimum":1},"expected_card_revision":{"type":"integer","minimum":1}},"required":["idempotency_key","board_id","card_id","expected_board_revision","expected_layout_revision","expected_card_revision"],"additionalProperties":false,"oneOf":[{"required":["before_card_id"]},{"required":["after_card_id"]}]}`),
	}
}
