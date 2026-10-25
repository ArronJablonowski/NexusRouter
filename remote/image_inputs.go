package remote

import "github.com/ArronJablonowski/NexusRouter/attachments"

// HasImageInputs inspects only valid durable user upload frames, including
// delegated direct-conversation inputs. It grants no model/tool authority.
func HasImageInputs(t Task) bool {
	has := func(text string) bool {
		_, files := attachments.Decode(text)
		for _, f := range files {
			if f.Image != "" {
				return true
			}
		}
		return false
	}
	if has(t.Prompt) {
		return true
	}
	for _, m := range t.Messages {
		if m.Role == "user" && has(m.Content) {
			return true
		}
	}
	return false
}
