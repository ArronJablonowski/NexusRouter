package codexbridge

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestLaunchObservationMissingIsUnknown(t *testing.T) {
	r, err := InspectLaunchConfig(json.RawMessage(`{"config":{}}`), []string{"hooks"})
	if err != nil || r.ExpectedFeatures != 1 || r.ObservedFeatures != 0 || r.MCPEntriesKnown || r.PluginEntriesKnown || r.ProjectContextDisabled || r.NotifyDisabled || r.WebSearchDisabled {
		t.Fatalf("missing controls became safe: %+v %v", r, err)
	}
}

func TestLaunchObservationOnlyProjectsMetadata(t *testing.T) {
	r, err := InspectLaunchConfig(json.RawMessage(`{"config":{"features":{"hooks":false,"skip_host_skill_discovery":true,"extra":true},"mcp_servers":{"private-name":{"url":"secret-value"}},"plugins":{},"project_doc_max_bytes":0,"notify":[],"web_search":"disabled","model_providers":{"private":{"api_key":"never-return"}}}}`), []string{"hooks", "skip_host_skill_discovery"})
	if err != nil || r.ExpectedFeatures != 2 || r.MatchingFeatures != 2 || r.AdditionalFeatures != 1 || !r.MCPEntriesKnown || r.MCPEntries != 1 || !r.PluginEntriesKnown || r.PluginEntries != 0 || !r.ProjectContextDisabled || !r.NotifyDisabled || !r.WebSearchDisabled {
		t.Fatalf("metadata: %+v %v", r, err)
	}
}

func TestLaunchObservationRejectsMalformedAndDoesNotDefaultFlags(t *testing.T) {
	for _, raw := range []string{`null`, `{"config":null}`, `{"config":{"features":[]}}`, `{"config":{"features":{"hooks":false,"hooks":true}}}`, `{"CONFIG":{}}`} {
		if _, err := InspectLaunchConfig(json.RawMessage(raw), []string{"hooks"}); !errors.Is(err, ErrLaunchObservation) {
			t.Fatalf("accepted malformed result: %v", err)
		}
	}
	for _, raw := range []string{`{"config":{"features":{"hooks":null}}}`, `{"config":{"features":{"hooks":"false"}}}`, `{"config":{"features":{"hooks":true}}}`} {
		r, err := InspectLaunchConfig(json.RawMessage(raw), []string{"hooks"})
		if err != nil || r.MatchingFeatures != 0 {
			t.Fatalf("unsafe flag accepted: %+v %v", r, err)
		}
	}
}
