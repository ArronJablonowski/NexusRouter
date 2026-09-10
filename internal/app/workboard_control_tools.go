package app

import (
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func workboardControlMutationSpec(name string) providers.Tool {
	description := "Record a durable pause request for one active card after operator approval; the MVP worker only observes this flag and does not yet pause, so a committed request is not a pause acknowledgement."
	if name == "workboard_request_cancel" {
		description = "Request cancellation of one active card after operator approval; a committed request is not verified cancellation finalization."
	}
	return providers.Tool{Name: name, Description: description, Parameters: workboardControlRequestSchema()}
}

func workboardControlRequestSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"card_id":` + workboardIDSchema + `,"expected_card_revision":{"type":"integer","minimum":1}},"required":["idempotency_key","board_id","card_id","expected_card_revision"],"additionalProperties":false}`)
}
