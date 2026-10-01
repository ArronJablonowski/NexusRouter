package api

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
)

var chatCapability = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Routing metadata constrains selection, never runtime registration or authority.
func decodeChatRouting(raw json.RawMessage, req *app.Request) error {
	bad := errors.New("invalid routing constraints")
	fields, err := chatObject(raw, "difficulty", "domain", "profile", "context_tokens", "max_cost", "local_required", "capabilities")
	if err != nil {
		return bad
	}
	if v, ok := fields["difficulty"]; ok {
		if chatString(v, &req.HarnessDifficulty) != nil || req.HarnessID == "" {
			return bad
		}
		switch req.HarnessDifficulty {
		case "unknown", "easy", "medium", "hard":
		default:
			return bad
		}
	}
	for name, target := range map[string]*string{"domain": &req.Domain, "profile": &req.Profile} {
		if value, ok := fields[name]; ok {
			if chatString(value, target) != nil || *target == "" || len(*target) > 128 || strings.TrimSpace(*target) != *target || strings.ContainsFunc(*target, unicode.IsControl) {
				return bad
			}
		}
	}
	if v, ok := fields["context_tokens"]; ok {
		if string(v) == "null" || json.Unmarshal(v, &req.ContextTokens) != nil || req.ContextTokens < 1 || req.ContextTokens > 1<<24 {
			return bad
		}
	}
	if v, ok := fields["max_cost"]; ok {
		if string(v) == "null" || json.Unmarshal(v, &req.MaxCost) != nil || req.MaxCost < 0 || math.IsNaN(req.MaxCost) || math.IsInf(req.MaxCost, 0) {
			return bad
		}
	}
	if v, ok := fields["local_required"]; ok {
		if string(v) != "true" && string(v) != "false" {
			return bad
		}
		req.LocalRequired = string(v) == "true"
	}
	if v, ok := fields["capabilities"]; ok {
		if string(v) == "null" || json.Unmarshal(v, &req.Capabilities) != nil || len(req.Capabilities) > 128 {
			return bad
		}
		seen := map[string]bool{}
		for _, c := range req.Capabilities {
			if !chatCapability.MatchString(c) || seen[c] {
				return bad
			}
			seen[c] = true
		}
	}
	return nil
}
