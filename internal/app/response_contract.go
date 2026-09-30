package app

import "github.com/ArronJablonowski/NexusRouter/responsecontract"

// Select only the current user turn before adding memories, skills, history or
// host reminders. Those sources cannot introduce a completion requirement.
func responseInstructions(r Request) string {
	if r.runtimeHostAdmission != nil || r.delegatedParent != "" {
		return ""
	}
	if len(r.Messages) == 0 {
		return r.Prompt
	}
	last := r.Messages[len(r.Messages)-1]
	if last.Role != "user" {
		return ""
	}
	return last.Content
}

func responseContract(r Request) responsecontract.Contract {
	return responsecontract.Infer(responseInstructions(r))
}
