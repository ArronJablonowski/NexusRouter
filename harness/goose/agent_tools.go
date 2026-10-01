package goose

import (
	"encoding/json"
	"regexp"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

type agentTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func validateAgentTools(tools []providers.Tool) error {
	_, e := agentToolEntries(tools)
	return e
}

func agentToolEntries(tools []providers.Tool) ([]agentTool, error) {
	if len(tools) < 1 || len(tools) > 128 {
		return nil, ErrProjection
	}
	entries := make([]agentTool, 0, len(tools))
	seen := map[string]bool{}
	for _, tool := range tools {
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(tool.Name) || seen[tool.Name] || !utf8.ValidString(tool.Description) || len(tool.Description) > 65536 || !wirejson.Unique(tool.Parameters) {
			return nil, ErrProjection
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(tool.Parameters, &schema) != nil || string(schema["type"]) != `"object"` {
			return nil, ErrProjection
		}
		seen[tool.Name] = true
		entries = append(entries, agentTool{tool.Name, tool.Description, tool.Parameters})
	}
	body, e := json.Marshal(entries)
	if e != nil || len(body) > MaxRecordBytes/2 {
		return nil, ErrProjection
	}
	return entries, nil
}
