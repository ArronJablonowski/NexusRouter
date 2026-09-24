package webui

import (
	"encoding/json"
	"testing"
)

func TestModelInventoryEvidenceFallbackContract(t *testing.T) {
	var raw map[string]json.RawMessage
	decodeFixture(t, "inspections.json", &raw)
	var fixture struct {
		Models ModelInspectionPage `json:"models"`
	}
	if err := json.Unmarshal(raw["models"], &fixture.Models); err != nil {
		t.Fatal(err)
	}
	rule := RoutingEvidenceFallbackInspection{Domain: "code", Profile: "default", SourceDomain: "coding", SourceProfile: "benchmark"}
	fixture.Models.EvidenceFallbacks = []RoutingEvidenceFallbackInspection{rule}
	if fixture.Models.Validate() != nil {
		t.Fatal("bounded fallback rejected")
	}
	compiler, location := compileInspectionSchema(t)
	validateSchemaValue(t, compiler, location+"#/$defs/model_inspection_page", marshalInspection(t, fixture.Models), true)
	fixture.Models.EvidenceFallbacks = append(fixture.Models.EvidenceFallbacks, rule)
	if fixture.Models.Validate() == nil {
		t.Fatal("ambiguous target accepted")
	}
	fixture.Models.EvidenceFallbacks = []RoutingEvidenceFallbackInspection{{Domain: "code", Profile: "default", SourceDomain: "private\nlabel", SourceProfile: "benchmark"}}
	if fixture.Models.Validate() == nil {
		t.Fatal("invalid task selector accepted")
	}
	validateSchemaValue(t, compiler, location+"#/$defs/model_inspection_page", marshalInspection(t, fixture.Models), false)
	fixture.Models.EvidenceFallbacks = make([]RoutingEvidenceFallbackInspection, 129)
	for i := range fixture.Models.EvidenceFallbacks {
		fixture.Models.EvidenceFallbacks[i] = rule
	}
	if fixture.Models.Validate() == nil {
		t.Fatal("unbounded fallback catalog accepted")
	}
	validateSchemaValue(t, compiler, location+"#/$defs/model_inspection_page", marshalInspection(t, fixture.Models), false)
}
