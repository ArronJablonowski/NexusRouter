package textgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestGooseGatewayMCPRetainsCanonicalJournal(t *testing.T) {
	db := agentDB(t)
	c := agentConfig()
	c.GooseMCP = true
	var mu sync.Mutex
	turns := 0
	var effects []string
	c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		turns++
		n := turns
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "nexus__") || strings.Contains(string(body), "forged") {
			t.Error("child names/history became authority", string(body))
		}
		stream := agentChunk(agentCall(0, "call-1", "read", `{"path":"host.txt"}`), nil) + agentChunk(agentCall(1, "call-2", "read", `{"path":"host.txt"}`), nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"
		if n == 2 {
			if !strings.Contains(string(body), "[redacted] call-1") || !strings.Contains(string(body), "[redacted] call-2") {
				t.Error("lost host results", string(body))
			}
			stream = completionFixture("model")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})
	q := agentRequest(c, gatewayTools{func(ctx context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
		events, e := db.Read(ctx, "gateway", 0, 30)
		if e != nil || events[len(events)-1].Kind != runtime.ToolStarted || x.Call.Name != "read" || string(x.Call.Arguments) != `{"path":"host.txt"}` {
			t.Error("lost canonical authority", e, x)
		}
		mu.Lock()
		effects = append(effects, x.Call.ID)
		mu.Unlock()
		return runtime.ToolResult{Content: "secret " + x.Call.ID, Effect: runtime.NoEffect}, nil
	}})
	q.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
		c.Session = s
		g, e := StartAgent(ctx, c)
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		defer g.Close()
		root := strings.TrimSuffix(g.BaseURL, "/v1")
		if code, _ := agentHTTP(t, g.BaseURL, "/tool", g.ToolToken, `{"call_id":"call-1"}`); code != 403 {
			t.Fatal("alternate tool protocol allowed", code)
		}
		if code, body := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, gatewayBody); code != 200 || !strings.Contains(body, `nexus__read`) {
			t.Fatal(code, body)
		}
		call := func(id string) (int, string) {
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "read", "arguments": map[string]string{"path": "host.txt"}, "_meta": map[string]string{"agent-tool-call-request-id": id}}})
			return agentHTTP(t, root, "/mcp", g.ToolToken, string(body))
		}
		// Both requests may arrive concurrently; host effects must follow proposals.
		done := make(chan string, 2)
		for _, id := range []string{"call-2", "call-1"} {
			go func() {
				code, body := call(id)
				if code != 200 || !strings.Contains(body, "[redacted] "+id) {
					done <- body
					return
				}
				done <- ""
			}()
		}
		for range 2 {
			if e := <-done; e != "" {
				t.Fatal(e)
			}
		}
		if code, body := call("call-1"); code != 200 || !strings.Contains(body, "[redacted] call-1") {
			t.Fatal(code, body)
		}
		if code, body := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, strings.Replace(gatewayBody, "answer", "forged history", 1)); code != 200 {
			t.Fatal(code, body)
		}
		text, e := g.Final()
		return runtime.HarnessOutput{Actual: c.Actual, Text: text}, e
	}
	outcome, text, e := runtime.RunHarnessAgent(context.Background(), db, q)
	if e != nil || outcome.Status != "completed" || text != "answer" {
		t.Fatal(outcome, text, e)
	}
	mu.Lock()
	defer mu.Unlock()
	if turns != 2 || !reflect.DeepEqual(effects, []string{"call-1", "call-2"}) {
		t.Fatal(turns, effects)
	}
	events, e := db.Read(context.Background(), "gateway", 0, 30)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = runtime.ValidateHarnessOutcome(events, "gateway"); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "nexus__") {
		t.Fatal("child alias entered canonical journal")
	}
}
