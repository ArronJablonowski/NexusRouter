package runtime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestProviderDiagnosticMetadataPreservesFailureBoundary(t *testing.T) {
	cause := &providers.Failure{Code: "invalid_stream", StreamDetail: "upstream_error", Partial: true}
	data := providerFailureData(TaskFailed, "provider_failed_before_tools", cause)
	if data.Text != "" || data.ProviderStreamDetail != "upstream_error" || data.Code != "provider_failed_before_tools" {
		t.Fatal(data)
	}
	event := Event{Version: 1, ID: "failure", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 3, Time: time.Now(), Kind: TaskFailed, Data: data}
	body, err := event.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var decoded Event
	if json.Unmarshal(body, &decoded) != nil || decoded.Validate() != nil || decoded.Data.ProviderStreamDetail != "upstream_error" {
		t.Fatal("diagnostic lost", string(body))
	}
	for _, kind := range []Kind{TaskCompleted, TaskCanceled, ModelDelta} {
		invalid := event
		invalid.Kind = kind
		invalid.TurnID = "turn"
		if invalid.Validate() == nil {
			t.Fatal("diagnostic accepted on", kind)
		}
	}
	invalid := event
	invalid.Data.ProviderStreamDetail = "private upstream body"
	if invalid.Validate() == nil {
		t.Fatal("unbounded diagnostic accepted")
	}
	if got := providerFailureData(TaskCanceled, "canceled", cause); got.Text != "" || got.ProviderStreamDetail != "" {
		t.Fatal(got)
	}
	if got := providerFailureData(TaskFailed, "execution_failed", &providers.Failure{StreamDetail: "private upstream body"}); got.ProviderStreamDetail != "" {
		t.Fatal(got)
	}
}
