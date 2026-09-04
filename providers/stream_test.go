package providers

import (
	"errors"
	"strings"
	"testing"
)

func TestIncompleteStreams(t *testing.T) {
	for _, tc := range []struct {
		name, kind, body string
		partial          bool
	}{
		{"empty", "sse", "", false},
		{"premature done", "sse", "data: [DONE]\n\n", false},
		{"missing marker", "sse", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n", true},
		{"malformed", "sse", "data: {invalid}\n\n", false},
		{"oversized line", "sse", strings.Repeat("x", 1<<20), false},
		{"missing done", "ollama", `{"message":{"content":"hi"},"done":false}` + "\n", true},
		{"negative usage", "ollama", `{"done":true,"eval_count":-1}` + "\n", false},
		{"truncated call", "ollama", `{"message":{"tool_calls":[{"function":{"name":"lookup","arguments":{}}}]}}` + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emit := func(c Chunk) error {
				if c.Done || c.ToolCall != nil {
					t.Error("released completion or tool from incomplete stream")
				}
				return nil
			}
			var err error
			if tc.kind == "sse" {
				err = readSSE(strings.NewReader(tc.body), emit)
			} else {
				err = readOllama(strings.NewReader(tc.body), emit)
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Partial != tc.partial {
				t.Fatalf("unexpected failure: %+v", err)
			}
		})
	}
}

func TestDuplicateToolIDsRejected(t *testing.T) {
	body := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"same","function":{"name":"a","arguments":"{}"}},{"index":1,"id":"same","function":{"name":"b","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	if err := readSSE(strings.NewReader(body), func(Chunk) error { t.Error("invalid batch released"); return nil }); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
}
