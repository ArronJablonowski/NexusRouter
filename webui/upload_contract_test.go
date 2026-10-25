package webui

import (
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"os"
	"testing"
)

func TestUploadContractAndSchema(t *testing.T) {
	schema, e := os.ReadFile("schema/v1.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var doc any
	json.Unmarshal(schema, &doc)
	c := jsonschema.NewCompiler()
	if e = c.AddResource("https://nexusrouter.local/schema/webui/v1", doc); e != nil {
		t.Fatal(e)
	}
	r := ChatRequest{Version: 1, Action: ChatSubmit, IdempotencyKey: "upload-contract-key-01", Text: "Summarize", Files: []attachments.File{{Name: "example.csv", Data: []byte("value,42")}}}
	if r.Validate() != nil {
		t.Fatal("valid upload rejected")
	}
	raw, _ := json.Marshal(r)
	validateSchemaValue(t, c, "https://nexusrouter.local/schema/webui/v1#/$defs/chat_request", raw, true)
	r.Action = ChatCancelSubmission
	r.Text = ""
	r.SubmissionID = "submission"
	if r.Validate() == nil {
		t.Fatal("cancel carried files")
	}
	raw, _ = json.Marshal(r)
	validateSchemaValue(t, c, "https://nexusrouter.local/schema/webui/v1#/$defs/chat_request", raw, false)
	r.Action = ChatSubmit
	r.SubmissionID = ""
	r.Text = "Summarize"
	r.Files[0].Name = "../private"
	if r.Validate() == nil {
		t.Fatal("path filename accepted")
	}
	raw, _ = json.Marshal(r)
	validateSchemaValue(t, c, "https://nexusrouter.local/schema/webui/v1#/$defs/chat_request", raw, false)
}
