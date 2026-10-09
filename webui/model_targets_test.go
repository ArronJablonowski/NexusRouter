package webui

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"os"
	"strings"
	"testing"
)

func TestModelTargetsContractRejectsAmbiguousIDs(t *testing.T) {
	p := ModelTargets{Version: 1, Hostname: "qa-host", Models: []ModelTarget{{ID: "a", Model: "Same model"}, {ID: "b", Model: "Same model"}}}
	if p.Validate() != nil {
		t.Fatal("names may share distinct IDs")
	}
	p.Models[1].ID = "a"
	if p.Validate() == nil {
		t.Fatal("duplicate deployment IDs accepted")
	}
	p.Models = p.Models[:1]
	p.Models[0].ID = "auto"
	if p.Validate() == nil {
		t.Fatal("reserved selector accepted")
	}
	p.Models[0].ID = "a"
	p.Hostname = strings.Repeat("x", 254)
	if p.Validate() == nil {
		t.Fatal("unbounded hostname accepted")
	}
}

func TestPublishedModelTargetsSchema(t *testing.T) {
	body, e := os.ReadFile("schema/v1.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		t.Fatal("schema parse failed")
	}
	c := jsonschema.NewCompiler()
	if c.AddResource("https://nexusrouter.local/schema/webui/v1", doc) != nil {
		t.Fatal("schema resource failed")
	}
	data, _ := json.Marshal(ModelTargets{Version: 1, Hostname: "qa-host", Models: []ModelTarget{{ID: "coder", Model: "Qwen Coder", Local: true}}})
	validateSchemaValue(t, c, "https://nexusrouter.local/schema/webui/v1#/$defs/model_targets", data, true)
	validateSchemaValue(t, c, "https://nexusrouter.local/schema/webui/v1#/$defs/model_targets", json.RawMessage(`{"version":1,"hostname":"qa-host","models":[],"address":"http://untrusted"}`), false)
}
