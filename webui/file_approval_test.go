package webui

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"os"
	"testing"
)

func TestFileCreateApprovalSchemaAndRuntime(t *testing.T) {
	body, e := os.ReadFile("schema/v1.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var doc any
	json.Unmarshal(body, &doc)
	c := jsonschema.NewCompiler()
	location := "https://nexusrouter.local/schema/webui/v1"
	if c.AddResource(location, doc) != nil {
		t.Fatal("schema")
	}
	for _, test := range []struct {
		args  string
		valid bool
	}{{`{"path":"report.md","content":"# Hello\n<script>text only</script>"}`, true}, {`{"path":"nested/report.md","content":""}`, true}, {`{"path":"../private","content":"x"}`, false}, {`{"path":"/private","content":"x"}`, false}, {`{"path":"report.md"}`, false}, {`{"path":"report.md","content":null}`, false}, {`{"path":"report.md","content":"x","root":"/"}`, false}} {
		p := ApprovalProposal{Version: 1, Kind: "file_create", Arguments: json.RawMessage(test.args)}
		if (p.Validate() == nil) != test.valid {
			t.Fatal("runtime", test.args)
		}
		b, _ := json.Marshal(p)
		validateSchemaValue(t, c, location+"#/$defs/approval_proposal", b, test.valid)
	}
	p := ApprovalProposal{Version: 1, Kind: "file_create", BoardID: "unrelated", Arguments: json.RawMessage(`{"path":"r.md","content":"x"}`)}
	if p.Validate() == nil {
		t.Fatal("unrelated authority fields accepted")
	}
}
