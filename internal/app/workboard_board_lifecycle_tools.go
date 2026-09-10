package app

import (
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func workboardBoardReviseMutationSpec() providers.Tool {
	return providers.Tool{
		Name:        "workboard_revise_board",
		Description: "Revise local Kanban board metadata using its exact revision after operator approval.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"title":{"type":"string","minLength":1,"maxLength":256},"description":{"type":"string","maxLength":65536},"expected_board_revision":{"type":"integer","minimum":1}},"required":["idempotency_key","board_id","expected_board_revision"],"additionalProperties":false,"anyOf":[{"required":["title"]},{"required":["description"]}]}`),
	}
}

func workboardBoardArchiveMutationSpec() providers.Tool {
	return providers.Tool{
		Name:        "workboard_archive_board",
		Description: "Archive one local Kanban board using its exact revision after operator approval.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"expected_board_revision":{"type":"integer","minimum":1}},"required":["idempotency_key","board_id","expected_board_revision"],"additionalProperties":false}`),
	}
}
