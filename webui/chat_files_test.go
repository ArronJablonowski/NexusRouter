package webui

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestChatFilePublishedSchemaMatchesRuntime(t *testing.T) {
	body, e := os.ReadFile("schema/v1.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		t.Fatal("schema")
	}
	c := jsonschema.NewCompiler()
	location := "https://nexusrouter.local/schema/webui/v1"
	if c.AddResource(location, doc) != nil {
		t.Fatal("resource")
	}
	for _, test := range []struct {
		name, kind string
		size       int
		valid      bool
	}{{"car.png", "image/png", 123, true}, {"notes.html", "text/plain", 0, true}, {"../private", "text/plain", 1, false}, {"..", "text/plain", 1, false}, {"bad\nheader", "text/plain", 1, false}, {"script.svg", "image/svg+xml", 100, false}, {"huge.png", "image/png", 33554433, false}} {
		f := ChatFile{ID: "file-one", TaskID: "task", Name: test.name, MediaType: test.kind, Size: test.size}
		if (f.Validate() == nil) != test.valid {
			t.Fatal("runtime", f)
		}
		b, _ := json.Marshal(f)
		validateSchemaValue(t, c, location+"#/$defs/chat_file", b, test.valid)
	}
}
