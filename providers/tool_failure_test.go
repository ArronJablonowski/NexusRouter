package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func failedMessages() []Message {
	return []Message{{Role: "user", Content: "lookup"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{}`)}}}, {Role: "tool", ToolCallID: "call", Content: `{"error":"missing"}`, ToolFailed: true}}
}

func TestToolFailedOnlyValidForPairedTool(t *testing.T) {
	if ValidateMessages(failedMessages()) != nil {
		t.Fatal("failed paired tool rejected")
	}
	for _, role := range []string{"system", "user", "assistant"} {
		if ValidateMessages([]Message{{Role: role, Content: "x", ToolFailed: true}}) == nil {
			t.Fatal(role)
		}
	}
	m := failedMessages()
	m[2].ToolCallID = "other"
	if ValidateMessages(m) == nil {
		t.Fatal("orphan failed tool accepted")
	}
}

func TestHTTPToolFailureFramingAndOwnedInput(t *testing.T) {
	for _, kind := range []string{"ollama", "openai_compatible"} {
		t.Run(kind, func(t *testing.T) {
			messages := failedMessages()
			before, _ := json.Marshal(messages)
			p := fixtureProvider(t, kind, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) != 3 {
					t.Fatal("invalid wire messages")
				}
				result := body.Messages[2]
				if result["content"] != toolFailurePrefix+messages[2].Content {
					t.Error(result)
				}
				if _, exists := result["tool_failed"]; exists {
					t.Error("unsupported status field leaked")
				}
				if kind == "ollama" {
					fmt.Fprintln(w, `{"message":{"content":"okay"},"done":true,"done_reason":"stop"}`)
				} else {
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"okay\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			})
			if err := p.Stream(context.Background(), Request{Model: "fixture", Messages: messages}, func(Chunk) error { return nil }); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(messages)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestToolFailureContextIncludesWireFraming(t *testing.T) {
	r := Request{Messages: failedMessages()}
	actual, err := EstimateContext(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(struct {
		Messages []Message
		Tools    []Tool
		Schema   json.RawMessage
	}{r.Messages, r.Tools, r.JSONSchema})
	if actual != len(b)+1024+len(toolFailurePrefix)+1 {
		t.Fatal("wire overhead omitted", actual, len(b))
	}
	original := r.Messages[2].Content
	r.Messages[2].ToolFailed = false
	if ToolResultContent(r.Messages[2]) != original {
		t.Fatal("successful content changed")
	}
}
