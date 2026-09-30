package webui

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestPublishedBrowserAuthenticationSchemaAndFixtures(t *testing.T) {
	schemaBody, err := os.ReadFile("schema/browser-auth-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	compiler := jsonschema.NewCompiler()
	if json.Unmarshal(schemaBody, &document) != nil || compiler.AddResource("https://nexusrouter.local/schema/webui/browser-auth-v1", document) != nil {
		t.Fatal("invalid browser authentication schema")
	}
	fixtureBody, err := os.ReadFile("testdata/v1/browser-auth.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if json.Unmarshal(fixtureBody, &fixtures) != nil || len(fixtures) != 7 {
		t.Fatal("invalid browser authentication fixtures")
	}
	for definition, fixture := range fixtures {
		validateSchemaValue(t, compiler, "https://nexusrouter.local/schema/webui/browser-auth-v1#/$defs/"+definition, fixture, true)
	}
	validateSchemaValue(t, compiler, "https://nexusrouter.local/schema/webui/browser-auth-v1#/$defs/approval_request", json.RawMessage(`{"version":1,"display_code":"abcdefgh"}`), false)
}
