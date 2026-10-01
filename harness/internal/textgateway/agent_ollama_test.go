package textgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

const nativeAgentCall = `{"model":"model","message":{"role":"assistant","content":"","tool_calls":[{"function":{"index":0,"name":"read","arguments":{"path":"host.txt","number":9007199254740993}}}]},"done":false}` + "\n" + `{"model":"model","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":3}` + "\n"

func TestOllamaAgentVerifiesNativeTools(t *testing.T) {
	a, e := ollamaAgentCompletion(strings.NewReader(nativeAgentCall), "model")
	if e != nil || len(a.Calls) != 1 || a.Calls[0].Name != "read" || !strings.Contains(string(a.Calls[0].Arguments), "9007199254740993") || a.Usage == nil || a.Usage.InputTokens != 10 {
		t.Fatal(a, e)
	}
	typed := strings.Replace(nativeAgentCall, `"tool_calls":[{"function":`, `"tool_calls":[{"type":"function","id":"native-id","function":`, 1)
	if got, err := ollamaAgentCompletion(strings.NewReader(typed), "model"); err != nil || len(got.Calls) != 1 {
		t.Fatal("documented function type rejected", got, err)
	}
	b, e := ollamaAgentCompletion(strings.NewReader(nativeAgentCall), "model")
	if e != nil || a.Calls[0].ID == b.Calls[0].ID {
		t.Fatal("call IDs reused across turns", e)
	}
	for _, tc := range []struct{ name, body string }{
		{"missing_terminal", strings.Split(nativeAgentCall, "\n")[0] + "\n"},
		{"wrong_model", strings.Replace(nativeAgentCall, `"model":"model"`, `"model":"fallback"`, 1)},
		{"after_done", nativeAgentCall + nativeAgentCall},
		{"length", strings.Replace(nativeAgentCall, `"stop"`, `"length"`, 1)},
		{"duplicate_args", strings.Replace(nativeAgentCall, `"path":"host.txt"`, `"path":"host.txt","path":"other"`, 1)},
		{"string_args", strings.Replace(nativeAgentCall, `{"path":"host.txt","number":9007199254740993}`, `"{}"`, 1)},
		{"thought", strings.Replace(nativeAgentCall, `"content":""`, `"content":"","thinking":"hidden"`, 1)},
		{"media", strings.Replace(nativeAgentCall, `"content":""`, `"content":"","images":["bytes"]`, 1)},
		{"partial_usage", strings.Replace(nativeAgentCall, `,"eval_count":3`, "", 1)},
		{"null_usage", strings.Replace(nativeAgentCall, `"eval_count":3`, `"eval_count":null`, 1)},
		{"negative_usage", strings.Replace(nativeAgentCall, `"eval_count":3`, `"eval_count":-1`, 1)},
		{"case_alias", strings.Replace(nativeAgentCall, `"model":`, `"MODEL":`, 1)},
		{"error", strings.Replace(nativeAgentCall, `"done":false`, `"done":false,"error":"failure"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, e := ollamaAgentCompletion(strings.NewReader(tc.body), "model")
			if e == nil || got.Text != "" || len(got.Calls) != 0 || len(got.Stream) != 0 || got.Usage != nil {
				t.Fatal("invalid native stream escaped", got, e)
			}
		})
	}
	if got, e := ollamaAgentCompletion(failingAgentReader{strings.NewReader(nativeAgentCall)}, "model"); e == nil || len(got.Calls) != 0 {
		t.Fatal("unclean EOF accepted")
	}
}

func TestAgentGatewayNativeOllamaConversation(t *testing.T) {
	db := agentDB(t)
	c := agentConfig()
	c.UpstreamProtocol = "ollama"
	c.BaseURL = "http://provider.invalid"
	var dispatches, effects atomic.Int32
	c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
		n := dispatches.Add(1)
		b, _ := io.ReadAll(r.Body)
		if r.URL.String() != "http://provider.invalid/api/chat" || r.Header.Get("Accept") != "application/x-ndjson" || strings.Contains(string(b), "forged") {
			t.Error("wrong native dispatch", r.URL, string(b))
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(b, &fields)
		if string(fields["think"]) != "false" || fields["max_tokens"] != nil || fields["stream_options"] != nil || !strings.Contains(string(fields["options"]), `"num_ctx":32768`) || !strings.Contains(string(fields["options"]), `"num_predict":1024`) {
			t.Error("lost native limits", string(b))
		}
		stream := nativeAgentCall
		if n == 2 {
			if !strings.Contains(string(b), `"tool_name":"read"`) || strings.Contains(string(b), `"tool_call_id"`) || !strings.Contains(string(b), `"arguments":{"number":9007199254740993,"path":"host.txt"}`) || !strings.Contains(string(b), "[redacted] result") {
				t.Error("native context lost binding", string(b))
			}
			stream = `{"model":"model","message":{"role":"assistant","content":"answer"},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":5}` + "\n"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})
	q := agentRequest(c, gatewayTools{func(ctx context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
		effects.Add(1)
		if !strings.Contains(string(x.Call.Arguments), "9007199254740993") {
			t.Error("numeric argument corrupted")
		}
		return runtime.ToolResult{Content: "secret result", Effect: runtime.NoEffect}, nil
	}})
	q.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
		c.Session = s
		g, e := StartAgent(ctx, c)
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		defer g.Close()
		code, body := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, gatewayBody)
		if code != 200 {
			t.Fatal(code, body)
		}
		proposal, e := VerifyAgentCompletion(strings.NewReader(body), c.Model)
		if e != nil || len(proposal.Calls) != 1 {
			t.Fatal(proposal, e)
		}
		data, _ := json.Marshal(map[string]string{"call_id": proposal.Calls[0].ID})
		for range 2 {
			if code, body = agentHTTP(t, g.BaseURL, "/tool", g.ToolToken, string(data)); code != 200 {
				t.Fatal(code, body)
			}
		}
		if code, body = agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, strings.Replace(gatewayBody, "answer", "forged continuation", 1)); code != 200 {
			t.Fatal(code, body)
		}
		text, e := g.Final()
		return runtime.HarnessOutput{Actual: c.Actual, Text: text}, e
	}
	if out, text, e := runtime.RunHarnessAgent(context.Background(), db, q); e != nil || out.Status != "completed" || text != "answer" || dispatches.Load() != 2 || effects.Load() != 1 {
		t.Fatal(out, text, e)
	}
	events, e := db.Read(context.Background(), "gateway", 0, 20)
	if e != nil {
		t.Fatal(e)
	}
	usage, e := runtime.ValidateHarnessAgentJournal(events, "gateway")
	if e != nil || usage == nil || usage.InputTokens != 30 || usage.OutputTokens != 8 {
		t.Fatal(usage, e)
	}
}

func TestOllamaAgentBoundsAndTextFraming(t *testing.T) {
	for _, count := range []int{2, maxAgentCalls, maxAgentCalls + 1} {
		calls := make([]any, count)
		for i := range calls {
			calls[i] = map[string]any{"type": "function", "function": map[string]any{"index": i, "name": "read", "arguments": map[string]string{"path": "fixture"}}}
		}
		body, _ := json.Marshal(map[string]any{"model": "model", "message": map[string]any{"role": "assistant", "tool_calls": calls}, "done": true, "done_reason": "stop"})
		got, e := ollamaAgentCompletion(strings.NewReader(string(body)+"\n"), "model")
		if count <= maxAgentCalls {
			if e != nil || len(got.Calls) != count {
				t.Fatal(count, got, e)
			}
		} else if e == nil {
			t.Fatal("call limit bypass")
		}
	}
	for _, counts := range []string{"", `,"prompt_eval_count":0,"eval_count":0`} {
		text := strings.Repeat("你好", 7000)
		message, _ := json.Marshal(map[string]string{"role": "assistant", "content": text})
		body := `{"model":"model","message":` + string(message) + `,"done":true,"done_reason":"stop"` + counts + "}\n"
		got, e := ollamaAgentCompletion(strings.NewReader(body), "model")
		if e != nil || got.Text != text || len(got.Calls) != 0 || (got.Usage != nil) != (counts != "") {
			t.Fatal("unicode/usage framing", e)
		}
	}
}
