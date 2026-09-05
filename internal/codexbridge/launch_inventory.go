package codexbridge

import (
	"bytes"
	"encoding/json"
)

// CheckLaunchInventories validates bounded, same-directory observations before
// a thread is created. It does not attest OS isolation or prevent later catalog
// changes. Private names, paths and diagnostics never appear in its errors.
func CheckLaunchInventories(cwd string, mcp, skills, hooks json.RawMessage) error {
	// Reuse the strict path, cardinality, error and duplicate-path validation.
	// The generated private override is deliberately discarded, never logged.
	if _, err := SkillDisableOverride(skills); err != nil {
		return ErrLaunchObservation
	}
	var skillList struct {
		Data []json.RawMessage `json:"data"`
	}
	var skillEntry struct {
		CWD    string            `json:"cwd"`
		Skills []json.RawMessage `json:"skills"`
	}
	if decodePayload(skills, &skillList) != nil || len(skillList.Data) != 1 || decodePayload(skillList.Data[0], &skillEntry) != nil || skillEntry.CWD != cwd {
		return ErrLaunchObservation
	}
	for _, raw := range skillEntry.Skills {
		var skill struct {
			Enabled *bool `json:"enabled"`
		}
		if decodePayload(raw, &skill) != nil || skill.Enabled == nil || *skill.Enabled {
			return ErrLaunchObservation
		}
	}
	var hookList struct {
		Data []json.RawMessage `json:"data"`
	}
	var hookEntry struct {
		CWD      string            `json:"cwd"`
		Hooks    []json.RawMessage `json:"hooks"`
		Errors   []json.RawMessage `json:"errors"`
		Warnings []json.RawMessage `json:"warnings"`
	}
	if decodePayload(hooks, &hookList) != nil || len(hookList.Data) != 1 || decodePayload(hookList.Data[0], &hookEntry) != nil || hookEntry.CWD != cwd ||
		hookEntry.Hooks == nil || len(hookEntry.Hooks) != 0 || hookEntry.Errors == nil || len(hookEntry.Errors) != 0 || hookEntry.Warnings == nil || len(hookEntry.Warnings) != 0 {
		return ErrLaunchObservation
	}
	var servers struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor json.RawMessage   `json:"nextCursor"`
	}
	if decodePayload(mcp, &servers) != nil || servers.Data == nil || len(servers.Data) > 64 ||
		(len(servers.NextCursor) != 0 && !bytes.Equal(bytes.TrimSpace(servers.NextCursor), []byte("null"))) {
		return ErrLaunchObservation
	}
	for _, raw := range servers.Data {
		var server struct {
			Tools map[string]json.RawMessage `json:"tools"`
		}
		// toolsAndAuthOnly responses can omit resources/resourceTemplates.
		// Only a known-empty tool map is asserted, not resource availability.
		if decodePayload(raw, &server) != nil || server.Tools == nil || len(server.Tools) != 0 {
			return ErrLaunchObservation
		}
	}
	return nil
}
