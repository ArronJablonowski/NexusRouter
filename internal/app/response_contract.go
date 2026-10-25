package app

import (
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/responsecontract"
)

// Select only the current user turn before adding memories, skills, history or
// host reminders. Those sources cannot introduce a completion requirement.
func responseInstructions(r Request) string {
	if r.runtimeHostAdmission != nil || r.delegatedParent != "" {
		return ""
	}
	if len(r.Messages) == 0 {
		text, _ := attachments.Decode(r.Prompt)
		return text
	}
	last := r.Messages[len(r.Messages)-1]
	if last.Role != "user" {
		return ""
	}
	text, _ := attachments.Decode(last.Content)
	return text
}

func responseContract(r Request) responsecontract.Contract {
	return responsecontract.Infer(responseInstructions(r))
}
