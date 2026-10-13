package webui

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"os"
	"testing"
)

func TestComfyApprovalPublishedSchemaMatchesRuntime(t *testing.T) {
	body, e := os.ReadFile("schema/v1.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		t.Fatal("schema JSON")
	}
	compiler := jsonschema.NewCompiler()
	location := "https://nexusrouter.local/schema/webui/v1"
	if compiler.AddResource(location, doc) != nil {
		t.Fatal("schema resource")
	}
	for _, test := range []struct {
		raw   string
		valid bool
	}{{`{"version":1,"kind":"comfy_workflow","arguments":{"key":"job","workflow":{"1":{"class_type":"EmptyImage","inputs":{}}}}}`, true}, {`{"version":1,"kind":"comfy_cancel","arguments":{"key":"job"}}`, true}, {`{"version":1,"kind":"comfy_cancel","arguments":{"key":"../job"}}`, false}, {`{"version":1,"kind":"comfy_workflow","arguments":{"key":"job","workflow":{"1":{}}}}`, false}, {`{"version":1,"kind":"comfy_cancel","arguments":{"key":"job","endpoint":"http://private"}}`, false}} {
		var p ApprovalProposal
		if json.Unmarshal([]byte(test.raw), &p) != nil {
			t.Fatal("fixture")
		}
		if (p.Validate() == nil) != test.valid {
			t.Fatal("runtime disagreement", test.raw)
		}
		validateSchemaValue(t, compiler, location+"#/$defs/approval_proposal", json.RawMessage(test.raw), test.valid)
	}
}
