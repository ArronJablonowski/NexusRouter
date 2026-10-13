package webui

import (
	"encoding/json"
	"testing"
)

func TestUnavailableModelInspectionSchemaMatchesRuntime(t *testing.T) {
	raw := json.RawMessage(`{"version":1,"availability":"unavailable","managed_residency":false,"specialists_allow_cloud":false,"local_unknown_size_count":0,"refresh_interval_ms":0,"local_providers":[],"models":[],"fitness":[]}`)
	var page ModelInspectionPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if err := page.Validate(); err != nil {
		t.Fatal(err)
	}
	compiler, location := compileInspectionSchema(t)
	validateSchemaValue(t, compiler, location+"#/$defs/model_inspection_page", raw, true)
}
