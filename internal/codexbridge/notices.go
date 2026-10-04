package codexbridge

import (
	"encoding/json"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
)

// compatibilityNotice admits informational notices on a checked connection.
// Feature notices require controls already verified on this connection.
// It never displays their payload, changes policy,
// or treats a notice as model output or successful work.
func (s *Session) compatibilityNotice(e codexrpc.Envelope) bool {
	if s.launchFeatures == nil || s.thread == "" {
		return false
	}
	has := func(name string) bool {
		for _, f := range s.launchFeatures {
			if f == name {
				return true
			}
		}
		return false
	}
	switch e.Method {
	case "account/updated":
		// Informational metadata never grants authorization or signals task success.
		return validHealthAccountNotice(e.Params)
	case "account/rateLimits/updated":
		// A sparse account-wide snapshot is not task usage, permission, or
		// evidence of success. Discard its bounded object without exposing
		// account metadata or using missing values as available budget.
		var n struct {
			RateLimits json.RawMessage `json:"rateLimits"`
		}
		var snapshot map[string]json.RawMessage
		return len(e.Params) <= 8192 && decodePayload(e.Params, &n) == nil && decodePayload(n.RateLimits, &snapshot) == nil
	case "deprecationNotice":
		var n struct {
			Summary *string `json:"summary"`
			Details *string `json:"details"`
		}
		if decodePayload(e.Params, &n) != nil || n.Summary == nil || len(*n.Summary) > 2048 || (n.Details != nil && len(*n.Details) > 4096) {
			return false
		}
		for name, summary := range map[string]string{
			"transcript_v2":       "`[features].transcript_v2` is deprecated and ignored.",
			"use_legacy_landlock": "`[features].use_legacy_landlock` is deprecated and will be removed soon.",
			"web_search_cached":   "`[features].web_search_cached` is deprecated because web search is enabled by default.",
			"web_search_request":  "`[features].web_search_request` is deprecated because web search is enabled by default.",
		} {
			if has(name) && *n.Summary == summary {
				return true
			}
		}
	case "warning":
		var n struct {
			Message  *string         `json:"message"`
			ThreadID json.RawMessage `json:"threadId"`
		}
		if decodePayload(e.Params, &n) != nil || n.Message == nil || len(*n.Message) > 8192 {
			return false
		}
		var thread string
		if json.Unmarshal(n.ThreadID, &thread) != nil || thread != s.thread {
			return false
		}
		const unstable = "Under-development features enabled: skip_host_skill_discovery. Under-development features are incomplete and may behave unpredictably. To suppress this warning, set `suppress_unstable_features_warning = true` in "
		if has("skip_host_skill_discovery") && strings.HasPrefix(*n.Message, unstable) {
			// The remaining installation-specific config path is never emitted.
			path := strings.TrimPrefix(*n.Message, unstable)
			return strings.HasPrefix(path, "/") && strings.HasSuffix(path, "config.toml.") && !strings.ContainsAny(path, "\r\n\x00")
		}
	}
	return false
}
