package codexbridge

// SupportedModel pins the Codex models admitted by this bridge. Catalog presence
// alone is not account entitlement; execution must still verify the exact model.
func SupportedModel(model string) bool {
	switch model {
	case "gpt-5.6-sol", "gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-daybreak-blue-latest":
		return true
	default:
		return false
	}
}
