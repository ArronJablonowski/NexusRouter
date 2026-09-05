package codexbridge

import (
	"bytes"
	"encoding/json"
	"errors"
)

var ErrLaunchObservation = errors.New("codex launch configuration observation failed")

// LaunchObservation contains only allowlisted metadata. It is NOT an inference
// admission or sandbox attestation. Configuration observations cannot prove
// effective tool absence, startup side effects or descendant containment.
// Missing values remain unknown, never an implicit false/empty/zero.
type LaunchObservation struct {
	ExpectedFeatures, ObservedFeatures, MatchingFeatures      int
	AdditionalFeatures                                        int
	MCPEntriesKnown, PluginEntriesKnown                       bool
	MCPEntries, PluginEntries                                 int
	ProjectContextDisabled, NotifyDisabled, WebSearchDisabled bool
}

// InspectLaunchConfig projects a bounded config/read result without returning
// raw values, extension names, provider URLs, paths, errors or secrets. Feature
// names come from trusted CLI metadata. Only skip_host_skill_discovery is
// expected true; every other named feature must be explicitly false.
func InspectLaunchConfig(result json.RawMessage, features []string) (LaunchObservation, error) {
	var report LaunchObservation
	if len(features) == 0 || len(features) > 256 {
		return report, ErrLaunchObservation
	}
	expected := make(map[string]bool, len(features))
	for _, name := range features {
		if !namePattern.MatchString(name) || expected[name] {
			return report, ErrLaunchObservation
		}
		expected[name] = true
	}
	var response struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if decodePayload(result, &response) != nil || response.Config == nil {
		return report, ErrLaunchObservation
	}
	report.ExpectedFeatures = len(features)
	var flags map[string]json.RawMessage
	if raw, ok := response.Config["features"]; ok && !bytes.Equal(raw, []byte("null")) {
		if json.Unmarshal(raw, &flags) != nil || flags == nil {
			return report, ErrLaunchObservation
		}
	}
	for name, value := range flags {
		if !expected[name] {
			report.AdditionalFeatures++
			continue
		}
		report.ObservedFeatures++
		want := []byte("false")
		if name == "skip_host_skill_discovery" {
			want = []byte("true")
		}
		if bytes.Equal(bytes.TrimSpace(value), want) {
			report.MatchingFeatures++
		}
	}
	for key, target := range map[string]*int{"mcp_servers": &report.MCPEntries, "plugins": &report.PluginEntries} {
		raw, ok := response.Config[key]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(raw, &entries) != nil || entries == nil {
			return LaunchObservation{}, ErrLaunchObservation
		}
		*target = len(entries)
		if key == "mcp_servers" {
			report.MCPEntriesKnown = true
		} else {
			report.PluginEntriesKnown = true
		}
	}
	report.ProjectContextDisabled = bytes.Equal(bytes.TrimSpace(response.Config["project_doc_max_bytes"]), []byte("0"))
	report.NotifyDisabled = bytes.Equal(bytes.TrimSpace(response.Config["notify"]), []byte("[]"))
	report.WebSearchDisabled = bytes.Equal(bytes.TrimSpace(response.Config["web_search"]), []byte(`"disabled"`))
	return report, nil
}
