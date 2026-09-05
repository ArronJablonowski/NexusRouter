package codexbridge

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// SkillDisableOverride builds a process-local override from one bounded
// skills/list result. It does not inspect paths or modify configuration. The
// returned string contains private paths and must never be logged. Observation
// of a subsequent process is still required; this is not inference admission.
func SkillDisableOverride(result json.RawMessage) (string, error) {
	var response struct {
		Data []json.RawMessage `json:"data"`
	}
	if decodePayload(result, &response) != nil || len(response.Data) != 1 {
		return "", ErrLaunchObservation
	}
	var entry struct {
		CWD    string            `json:"cwd"`
		Skills []json.RawMessage `json:"skills"`
		Errors []json.RawMessage `json:"errors"`
	}
	if decodePayload(response.Data[0], &entry) != nil {
		return "", ErrLaunchObservation
	}
	validPath := func(path string) bool {
		if len(path) > 4096 || !filepath.IsAbs(path) {
			return false
		}
		for _, c := range path {
			if unicode.IsControl(c) || c == unicode.ReplacementChar {
				return false
			}
		}
		return true
	}
	if !validPath(entry.CWD) || entry.Skills == nil || len(entry.Skills) > 64 || entry.Errors == nil || len(entry.Errors) != 0 {
		return "", ErrLaunchObservation
	}
	paths := make([]string, 0, len(entry.Skills))
	seen := make(map[string]bool, len(entry.Skills))
	for _, raw := range entry.Skills {
		var skill struct {
			Path    string `json:"path"`
			Enabled *bool  `json:"enabled"`
		}
		if decodePayload(raw, &skill) != nil || !validPath(skill.Path) || skill.Enabled == nil || seen[skill.Path] {
			return "", ErrLaunchObservation
		}
		seen[skill.Path] = true
		paths = append(paths, skill.Path)
	}
	sort.Strings(paths)
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		// JSON string escaping is valid TOML for these control-free paths.
		quoted, _ := json.Marshal(path)
		parts = append(parts, "{path="+string(quoted)+",enabled=false}")
	}
	arg := "skills.config=[" + strings.Join(parts, ",") + "]"
	if len(arg) > 16<<10 {
		return "", ErrLaunchObservation
	}
	return arg, nil
}
