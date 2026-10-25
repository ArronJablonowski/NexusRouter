package webui

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"os"
	"testing"
)

func TestQualityRatingSchemaAndRuntime(t *testing.T) {
	raw, err := os.ReadFile("schema/v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatal("schema")
	}
	c := jsonschema.NewCompiler()
	location := "https://nexusrouter.local/schema/webui/v1"
	if c.AddResource(location, doc) != nil {
		t.Fatal("schema resource")
	}
	for _, rating := range []int{0, 1, 5, 6, 10, 11} {
		r := FeedbackRequest{Version: 1, IdempotencyKey: "quality-schema-key", TaskID: "task", Action: FeedbackRecord, Rating: rating, Accepted: rating >= 6}
		valid := rating >= 1 && rating <= 10
		if (r.Validate() == nil) != valid {
			t.Fatal("runtime rating boundary", rating)
		}
		body, _ := json.Marshal(r)
		validateSchemaValue(t, c, location+"#/$defs/feedback_request", body, valid)
	}
	cost := 1.0
	r := FeedbackRequest{Version: 1, IdempotencyKey: "quality-schema-key", TaskID: "task", Action: FeedbackRecord, Rating: 9, Accepted: true, AttemptCost: &cost}
	if r.Validate() == nil {
		t.Fatal("rating trusted client-supplied cost")
	}
	body, _ := json.Marshal(r)
	validateSchemaValue(t, c, location+"#/$defs/feedback_request", body, false)
}

func TestExplicitInvalidRatingDoesNotBecomeLegacyCost(t *testing.T) {
	for _, raw := range []string{`{"rating":0,"attempt_cost":1}`, `{"rating":null,"attempt_cost":1}`, `{"rating":11}`, `{"rating":5.5}`} {
		var r FeedbackRequest
		if json.Unmarshal([]byte(raw), &r) == nil {
			t.Fatal("invalid explicit score decoded", raw)
		}
	}
}
