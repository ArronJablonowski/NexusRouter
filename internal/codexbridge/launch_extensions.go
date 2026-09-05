package codexbridge

import (
	"encoding/json"
	"sort"
	"strings"
)

// ExtensionDisableOverrides builds process-local TOML overrides from a bounded
// configuration observation. It does not launch, write config or admit a turn.
// Returned arguments contain private extension identifiers: never log them.
// The next process must be observed again; configuration may change meanwhile.
func ExtensionDisableOverrides(result json.RawMessage) ([]string, error) {
	var response struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if decodePayload(result, &response) != nil || response.Config == nil {
		return nil, ErrLaunchObservation
	}
	args := make([]string, 0, 2)
	total := 0
	for _, key := range []string{"mcp_servers", "plugins"} {
		var entries map[string]json.RawMessage
		if json.Unmarshal(response.Config[key], &entries) != nil || entries == nil || len(entries) > 64 {
			return nil, ErrLaunchObservation
		}
		names := make([]string, 0, len(entries))
		for name, raw := range entries {
			if len(name) == 0 || len(name) > 256 {
				return nil, ErrLaunchObservation
			}
			// Restrict the supported inventory rather than guessing at TOML
			// control/unicode escaping or CLI path-segment interpretation.
			for _, c := range name {
				if c < 32 || c > 126 {
					return nil, ErrLaunchObservation
				}
			}
			var entry map[string]json.RawMessage
			if json.Unmarshal(raw, &entry) != nil || entry == nil {
				return nil, ErrLaunchObservation
			}
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			quoted, _ := json.Marshal(name) // ASCII JSON quoting is valid TOML.
			parts = append(parts, string(quoted)+"={enabled=false}")
		}
		// Quote identifiers within a TOML value, not the CLI dotted-key path.
		arg := key + "={" + strings.Join(parts, ",") + "}"
		total += len(arg)
		if total > 32<<10 {
			return nil, ErrLaunchObservation
		}
		args = append(args, arg)
	}
	return args, nil
}
