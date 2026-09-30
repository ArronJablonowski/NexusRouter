package app

import (
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func workboardControlMutationSpec(name string) providers.Tool {
	description := "Request a safe-boundary pause for one active card after operator approval. A committed request is not a pause acknowledgement; the worker records acknowledgement separately when it reaches a safe boundary."
	if name == "workboard_request_resume" {
		description = "Request that one worker-acknowledged paused card resume after operator approval. A committed request is not a resume acknowledgement; the worker records acknowledgement separately before continuing."
	} else if name == "workboard_request_cancel" {
		description = "Request cancellation of one active card after operator approval; a committed request is not verified cancellation finalization."
	}
	return providers.Tool{Name: name, Description: description, Parameters: workboardControlRequestSchema()}
}

func workboardControlRequestSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"card_id":` + workboardIDSchema + `,"expected_card_revision":{"type":"integer","minimum":1}},"required":["idempotency_key","board_id","card_id","expected_card_revision"],"additionalProperties":false}`)
}
