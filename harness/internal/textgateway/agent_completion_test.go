package textgateway

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func agentChunk(delta string, finish any) string {
	body, _ := json.Marshal(map[string]any{"id": "agent", "model": "model", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": json.RawMessage(delta), "finish_reason": finish}}})
	return "data: " + string(body) + "\n\n"
}
func agentCall(index int, id, name, arguments string) string {
	body, _ := json.Marshal(map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": id, "type": "function", "function": map[string]string{"name": name, "arguments": arguments}}}})
	return string(body)
}
func agentFixture() string {
	return agentChunk(agentCall(0, "read-1", "read", `{"path":`), nil) +
		agentChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"test.txt\"}"}}]}`, nil) +
		agentChunk(agentCall(1, "write-1", "write", `{"value":9007199254740993}`), nil) +
		agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"
}

func TestAgentCompletionVerifiesFragmentedCalls(t *testing.T) {
	stream := agentFixture()
	got, err := VerifyAgentCompletion(strings.NewReader(stream), "model")
	if err != nil || len(got.Calls) != 2 || got.Text != "" || got.Usage != nil {
		t.Fatal(got, err)
	}
	if got.Calls[0].ID != "read-1" || got.Calls[0].Name != "read" || string(got.Calls[0].Arguments) != `{"path":"test.txt"}` || string(got.Calls[1].Arguments) != `{"value":9007199254740993}` {
		t.Fatal(got.Calls)
	}
	if _, err := verifyCompletion(strings.NewReader(stream), "model"); err == nil {
		t.Fatal("legacy text gateway admitted tools")
	}
	// Re-verifying the forwarded canonical stream yields identical proposals.
	forwarded, err := VerifyAgentCompletion(strings.NewReader(string(got.Stream)), "model")
	if err != nil || string(forwarded.Calls[0].Arguments) != string(got.Calls[0].Arguments) {
		t.Fatal(forwarded, err)
	}
	text, err := VerifyAgentCompletion(strings.NewReader(completionFixture("model")), "model")
	if err != nil || text.Text != "answer" || len(text.Calls) != 0 {
		t.Fatal(text, err)
	}
}

func TestAgentCompletionRejectsAmbiguousProposals(t *testing.T) {
	good := agentFixture()
	for _, tc := range []struct{ name, stream string }{
		{"wrong_finish", strings.Replace(good, `"finish_reason":"tool_calls"`, `"finish_reason":"stop"`, 1)},
		{"missing_done", strings.Replace(good, "data: [DONE]\n\n", "", 1)},
		{"duplicate_id", strings.Replace(good, `"id":"write-1"`, `"id":"read-1"`, 1)},
		{"skipped_index", strings.Replace(good, `"index":1`, `"index":2`, 1)},
		{"negative_index", strings.Replace(good, `"index":0,"type"`, `"index":-1,"type"`, 1)},
		{"changed_name", agentChunk(agentCall(0, "one", "read", `{`), nil) + agentChunk(agentCall(0, "one", "write", `}`), nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"changed_id", agentChunk(agentCall(0, "one", "read", `{`), nil) + agentChunk(agentCall(0, "two", "read", `}`), nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"duplicate_args", agentChunk(agentCall(0, "one", "read", `{"path":"safe","path":"other"}`), nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"nested_duplicate", agentChunk(agentCall(0, "one", "read", `{"a":{"b":1,"b":2}}`), nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"array_args", agentChunk(agentCall(0, "one", "read", `[]`), nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"null_args", agentChunk(agentCall(0, "one", "read", `null`), nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"oversize_args", agentChunk(agentCall(0, "one", "read", `{"a":"`+strings.Repeat("a", maxAgentArguments)+`"}`), nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"empty_calls", agentChunk(`{"tool_calls":[]}`, nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"missing_type", agentChunk(`{"tool_calls":[{"index":0,"id":"one","function":{"name":"read","arguments":"{}"}}]}`, nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"unknown_field", agentChunk(`{"tool_calls":[{"index":0,"id":"one","type":"function","authority":"yes","function":{"name":"read","arguments":"{}"}}]}`, nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"},
		{"tool_finish_without_calls", strings.Replace(completionFixture("model"), `"finish_reason":"stop"`, `"finish_reason":"tool_calls"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := VerifyAgentCompletion(strings.NewReader(tc.stream), "model")
			if err == nil || len(got.Calls) != 0 || got.Text != "" || len(got.Stream) != 0 || got.Usage != nil {
				t.Fatal("released invalid proposal", got, err)
			}
		})
	}
}

type failingAgentReader struct{ io.Reader }

func (r failingAgentReader) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	if e == io.EOF {
		e = io.ErrUnexpectedEOF
	}
	return n, e
}
func TestAgentCompletionRequiresCleanEOF(t *testing.T) {
	got, err := VerifyAgentCompletion(failingAgentReader{strings.NewReader(agentFixture())}, "model")
	if err == nil || len(got.Calls) != 0 {
		t.Fatal("released before clean EOF", got, err)
	}
}

func TestAgentCompletionCallCountAndUsage(t *testing.T) {
	for _, count := range []int{maxAgentCalls, maxAgentCalls + 1} {
		var stream strings.Builder
		for i := 0; i < count; i++ {
			stream.WriteString(agentChunk(agentCall(i, fmt.Sprint("call-", i), "read", `{}`), nil))
		}
		stream.WriteString(agentChunk(`{}`, "tool_calls"))
		stream.WriteString(`data: {"id":"agent","object":"chat.completion.chunk","model":"model","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}` + "\n\ndata: [DONE]\n\n")
		got, err := VerifyAgentCompletion(strings.NewReader(stream.String()), "model")
		if count == maxAgentCalls {
			if err != nil || len(got.Calls) != count || got.Usage == nil || got.Usage.InputTokens != 100 {
				t.Fatal(got, err)
			}
		} else if err == nil || len(got.Calls) != 0 || got.Usage != nil {
			t.Fatal("exceeded tool limit", got, err)
		}
	}
}
func TestAgentCompletionCanonicalKeys(t *testing.T) {
	for _, key := range []string{"id", "object", "model", "index", "delta", "finish_reason"} {
		stream := strings.Replace(agentFixture(), `"`+key+`":`, `"`+strings.ToUpper(key)+`":`, 1)
		if got, e := VerifyAgentCompletion(strings.NewReader(stream), "model"); e == nil || len(got.Calls) != 0 {
			t.Fatal("case alias forwarded", key, got, e)
		}
	}
}
