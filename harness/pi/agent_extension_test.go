package pi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/textgateway"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type extensionTools struct {
	invoke func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error)
}

func (x extensionTools) Execute(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
	panic("unscoped")
}
func (x extensionTools) ExecuteScoped(c context.Context, e runtime.ToolExecution) (runtime.ToolResult, error) {
	return x.invoke(c, e)
}
func (x extensionTools) ToolBehavior(string) runtime.ToolBehavior { return runtime.BehaviorReadOnly }
func extensionSchema() []providers.Tool {
	return []providers.Tool{{Name: "nexus_lookup", Description: "Read the fixture through the host", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}}
}
func TestAgentExtensionConfiguration(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:1234/v1", "https://127.0.0.1:1234/v1", "http://example.com:1234/v1", "http://user@127.0.0.1:1234/v1", "http://127.0.0.1:1234/v1?secret=x"} {
		body, e := renderAgentExtension(base, strings.Repeat("a", 32), time.Second, extensionSchema())
		if (e == nil) != (base == "http://127.0.0.1:1234/v1") {
			t.Fatal(base, e)
		}
		if e == nil && (!strings.Contains(string(body), `executionMode: "sequential"`) || !strings.Contains(string(body), `exposure: "model-only"`)) {
			t.Fatal("unscoped extension")
		}
	}
	tools := extensionSchema()
	tools[0].Parameters = json.RawMessage(`{"type":"object","type":"array"}`)
	if _, e := renderAgentExtension("http://127.0.0.1:1234/v1", strings.Repeat("a", 32), time.Second, tools); e == nil {
		t.Fatal("duplicate schema accepted")
	}
}

func TestNativePiHostToolExtension(t *testing.T) {
	for _, mode := range []string{"normal", "recoverable", "end"} {
		t.Run(mode, func(t *testing.T) { testNativePiHostToolExtension(t, mode, false) })
	}
}
func TestNativePiAgentTask(t *testing.T) {
	for _, mode := range []string{"normal", "recoverable", "end", "wrong_model", "cancel", "ollama", "host", "host_contract"} {
		t.Run(mode, func(t *testing.T) { testNativePiHostToolExtension(t, mode, true) })
	}
}
func testNativePiHostToolExtension(t *testing.T, mode string, production bool) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("native Pi qualification requires NEXUS_PI_NATIVE=1")
	}
	executable, e := exec.LookPath("pi")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	version, e := exec.CommandContext(ctx, executable, "--version").Output()
	if e != nil || strings.TrimSpace(string(version)) != SupportedVersion {
		t.Fatal("unsupported installed Pi", e)
	}
	db, e := telemetry.Open(ctx, filepath.Join(t.TempDir(), "events.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var requests, effects atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if mode == "cancel" {
			cancel()
			return
		}
		data, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(data), "host task") || !strings.Contains(string(data), "nexus_lookup") {
			t.Error("missing host context/tools", string(data))
		}
		if n == 2 && mode == "recoverable" && !strings.Contains(string(data), "Tool execution failed.") {
			t.Error("lost recoverable failure status")
		}
		if n == 2 && mode == "end" {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(data, &fields)
			if fields["tools"] != nil {
				t.Error("tools offered after EndToolUse")
			}
		}
		if n == 2 && !strings.Contains(string(data), "fixture result") {
			t.Error("missing host tool result")
		}
		if mode == "ollama" {
			if r.URL.Path != "/api/chat" {
				t.Error("wrong native endpoint", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			message := map[string]any{"role": "assistant", "content": "native tool answer"}
			if n == 1 {
				message = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "nexus_lookup", "arguments": map[string]string{"path": "fixture"}}}}}
			}
			body, _ := json.Marshal(map[string]any{"model": "model", "message": message, "done": true, "done_reason": "stop", "prompt_eval_count": 10, "eval_count": 3})
			fmt.Fprintf(w, "%s\n", body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "host-call-1", "type": "function", "function": map[string]string{"name": "nexus_lookup", "arguments": `{"path":"fixture"}`}}}}
		finish := "tool_calls"
		if n == 2 {
			delta = map[string]any{"role": "assistant", "content": "native tool answer"}
			finish = "stop"
		}
		for _, piece := range []map[string]any{{"delta": delta, "finish_reason": nil, "index": 0}, {"delta": map[string]string{}, "finish_reason": finish, "index": 0}} {
			responseModel := "model"
			if mode == "wrong_model" {
				responseModel = "fallback"
			}
			body, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": responseModel, "choices": []any{piece}})
			fmt.Fprintf(w, "data: %s\n\n", body)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	actual := harness.Identity{Version: 1, Harness: "pi", HarnessVersion: SupportedVersion, AdapterVersion: "extension-fixture", Provider: "nexus-test", Model: "model", ModelRevision: "fixture", ConfigSHA256: strings.Repeat("a", 64)}
	artifact, e := os.ReadFile(executable)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(artifact)
	var released atomic.Int32
	cfg := AgentConfig{Config: Config{Transport: http.DefaultTransport, TransportPolicySHA256: strings.Repeat("c", 64), ModelRevision: "fixture", Prices: &Prices{}, Executable: executable, ExecutableSHA256: hex.EncodeToString(digest[:]), Provider: "nexus-test", Model: "model", BaseURL: provider.URL + "/v1", ContextTokens: 32768, MaxOutputTokens: 1024, Timeout: 15 * time.Second, Messages: []providers.Message{{Role: "user", Content: "host task"}}, Admit: func(context.Context) (func(), error) { return func() { released.Add(1) }, nil }}, Tools: extensionSchema(), MaxTurns: 3}
	if mode == "ollama" {
		cfg.UpstreamProtocol = "ollama"
		cfg.BaseURL = provider.URL
	}
	if production {
		actual, e = cfg.Identity()
		if e != nil {
			t.Fatal(e)
		}
	}
	q := runtime.HarnessAgentRequest{Request: runtime.HarnessRequest{TaskID: "native-extension", SessionID: "native-extension", Attribution: runtime.HarnessAttribution{Identity: actual, Task: harness.TaskClass{Domain: "code", Profile: "fixture", Difficulty: "easy"}}, Messages: []providers.Message{{Role: "user", Content: "host task"}}, ContextTokens: 32768, MaxOutputBytes: 65536}, MaxTurns: 3, Tools: extensionTools{func(c context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
		effects.Add(1)
		events, e := db.Read(c, x.TaskID, 0, 10)
		if e != nil || len(events) != 4 || events[3].Kind != runtime.ToolStarted || x.Call.Name != "nexus_lookup" || string(x.Call.Arguments) != `{"path":"fixture"}` {
			t.Error("unbound native effect", e)
		}
		return runtime.ToolResult{Content: "fixture result", Effect: runtime.NoEffect, Failed: mode == "recoverable", Recoverable: mode == "recoverable", EndToolUse: mode == "end"}, nil
	}}}
	q.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
		g, e := textgateway.StartAgent(ctx, textgateway.AgentConfig{Config: textgateway.Config{BaseURL: provider.URL + "/v1", Model: "model", Transport: http.DefaultTransport, ContextTokens: 32768, MaxOutputTokens: 1024, Timeout: 15 * time.Second, Messages: q.Request.Messages}, Actual: actual, Session: s, Tools: extensionSchema()})
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		defer g.Close()
		dir := t.TempDir()
		module, e := renderAgentExtension(g.BaseURL, g.ToolToken, 15*time.Second, extensionSchema())
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		modulePath := filepath.Join(dir, "host-tools.js")
		if e = os.WriteFile(modulePath, module, 0600); e != nil {
			return runtime.HarnessOutput{}, e
		}
		settings := map[string]any{"compaction": map[string]bool{"enabled": false}, "retry": map[string]any{"enabled": false, "provider": map[string]int{"maxRetries": 0}}, "defaultTools": []string{}, "quietStartup": true}
		models := map[string]any{"providers": map[string]any{"nexus-test": map[string]any{"baseUrl": g.BaseURL, "api": "openai-completions", "models": []any{map[string]any{"id": "model", "name": "model", "reasoning": false, "input": []string{"text"}, "contextWindow": 32768, "maxTokens": 1024, "cost": Prices{}}}}}}
		for name, value := range map[string]any{"models.json": models, "auth.json": map[string]any{"nexus-test": map[string]string{"type": "api_key", "key": g.Token}}, "settings.json": settings} {
			body, _ := json.Marshal(value)
			if e = os.WriteFile(filepath.Join(dir, name), body, 0600); e != nil {
				return runtime.HarnessOutput{}, e
			}
		}
		processCtx, stop := context.WithCancel(ctx)
		defer stop()
		command := exec.CommandContext(processCtx, executable, "--mode", "rpc", "--no-session", "--offline", "--no-builtin-tools", "--tools", "nexus_lookup", "--no-extensions", "--extension", modulePath, "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--thinking", "off", "--system-prompt", "Use only the provided host tool then answer.", "--provider", "nexus-test", "--model", "model")
		command.Dir = dir
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "PI_CODING_AGENT_DIR=" + dir, "PI_OFFLINE=1", "PI_TELEMETRY=0", "NO_COLOR=1"}
		command.Stderr = io.Discard
		command.WaitDelay = 2 * time.Second
		input, e := command.StdinPipe()
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		output, e := command.StdoutPipe()
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		if e = command.Start(); e != nil {
			return runtime.HarnessOutput{}, e
		}
		defer func() { input.Close(); stop(); command.Wait(); output.Close() }()
		protocol, e := NewAgentProtocol("nexus-test", "model", 3, []string{"nexus_lookup"})
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		protocol.base.expectURL = g.BaseURL
		protocol.base.expectContext = 32768
		protocol.base.expectOutput = 1024
		sent := false
		encoder := json.NewEncoder(input)
		if e = encoder.Encode(map[string]string{"id": "state", "type": "get_state"}); e != nil {
			return runtime.HarnessOutput{}, e
		}
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), MaxRecordBytes)
		types := []string{}
		bytes := 0
		for scanner.Scan() {
			bytes += len(scanner.Bytes())
			if bytes > 16<<20 {
				return runtime.HarnessOutput{}, ErrProtocol
			}
			var event struct {
				Type    string
				Message struct{ Role, StopReason string }
				Error   string
			}
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				return runtime.HarnessOutput{}, ErrProtocol
			}
			types = append(types, event.Type)
			settled, protocolErr := protocol.Consume(scanner.Bytes())
			if protocolErr != nil {
				t.Log("rejected fixture RPC", string(scanner.Bytes()))
				return runtime.HarnessOutput{}, protocolErr
			}
			if protocol.Ready() && !sent {
				sent = true
				if e = encoder.Encode(map[string]string{"id": "prompt", "type": "prompt", "message": "Use the fixture tool then answer."}); e != nil {
					return runtime.HarnessOutput{}, e
				}
			}

			if event.Type == "tool_execution_end" {
				var result struct{ IsError bool }
				if json.Unmarshal(scanner.Bytes(), &result) != nil || result.IsError != (mode == "recoverable") {
					t.Error("Pi tool failure flag mismatch")
				}
			}
			if settled {
				text, e := g.Final()
				result, resultErr := protocol.Result()
				if resultErr != nil || result.Text != text {
					return runtime.HarnessOutput{}, ErrProtocol
				}
				if e != nil {
					t.Log("native lifecycle", types)
					t.Log("fixture terminal", string(scanner.Bytes()))
				}
				return runtime.HarnessOutput{Actual: actual, Text: text}, e
			}
		}
		t.Log("native lifecycle", types)
		return runtime.HarnessOutput{}, ErrRun
	}
	var out harness.Execution
	var text string
	if mode == "host" || mode == "host_contract" {
		q.Request.Privacy = "local_only"
		q.Execute = func(run context.Context, session *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
			result, err := RunAgent(run, cfg, "Use the fixture tool then answer.", session)
			if err == nil && mode == "host_contract" {
				// Host response validation happens before runtime commits success.
				err = fmt.Errorf("fixture response contract requires JSON")
			}
			return runtime.HarnessOutput{Actual: result.Identity, Text: result.Text}, err
		}
		out, text, e = runtime.RunHarnessAgent(ctx, db, q)
		events, readErr := db.Read(context.Background(), q.Request.TaskID, 0, 100)
		if readErr != nil || len(events) == 0 || events[0].Data.Privacy != "local_only" || released.Load() != 1 {
			t.Fatal("lost host policy or resource release", readErr, released.Load(), e, events)
		}
		if mode == "host_contract" {
			last := events[len(events)-1]
			if e == nil || out.Status != "" || text != "" || last.Kind != runtime.TaskFailed || last.Data.HarnessOutcome != nil || last.Data.Text != "" || requests.Load() != 2 || effects.Load() != 1 {
				t.Fatal("response contract failure committed success", out, last, e)
			}
			return
		}
	} else if production {
		task := Task{ID: q.Request.TaskID, SessionID: q.Request.SessionID, Prompt: "Use the fixture tool then answer.", Class: q.Request.Attribution.Task, MaxOutputBytes: 65536}
		var result TaskResult
		result, e = RunAgentTask(ctx, db, cfg, task, q.Tools)
		out, text = result.Execution, result.Result.Text
		if e == nil && result.Result.Identity != actual {
			t.Error("agent provenance mismatch")
		}
		if _, retryErr := RunAgentTask(ctx, db, cfg, task, q.Tools); retryErr == nil {
			t.Error("replayed native task")
		}
		if released.Load() != 1 {
			t.Error("resource release count", released.Load())
		}
	} else {
		out, text, e = runtime.RunHarnessAgent(ctx, db, q)
	}
	if mode == "wrong_model" || mode == "cancel" {
		if e == nil || out.Status != "" || text != "" || requests.Load() != 1 || effects.Load() != 0 {
			t.Fatal("failed native run accepted", out, text, e, requests.Load(), effects.Load())
		}
		events, readErr := db.Read(context.Background(), q.Request.TaskID, 0, 20)
		want := runtime.TaskFailed
		if mode == "cancel" {
			want = runtime.TaskCanceled
		}
		if readErr != nil || len(events) == 0 || events[len(events)-1].Kind != want || events[len(events)-1].Data.HarnessOutcome != nil {
			t.Fatal("lost failed lineage", events, readErr)
		}
		return
	}
	if e != nil || out.Status != "completed" || text != "native tool answer" || requests.Load() != 2 || effects.Load() != 1 {
		t.Fatal(out, text, e, requests.Load(), effects.Load())
	}
	events, e := db.Read(ctx, q.Request.TaskID, 0, 20)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = runtime.ValidateHarnessOutcome(events, q.Request.TaskID); e != nil {
		t.Fatal(e)
	}
}

func TestAgentExtensionBridgeContract(t *testing.T) {
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip("Node required for extension contract test")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "host-tools.mjs")
	source, e := renderAgentExtension("http://127.0.0.1:1234/v1", strings.Repeat("a", 32), time.Second, extensionSchema())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, source, 0600); e != nil {
		t.Fatal(e)
	}
	script := `import assert from 'node:assert/strict';
const {default: extension}=await import(process.argv[1]);
const hooks={},tools=[];let active=[];
extension({on:(name,fn)=>hooks[name]=fn,registerTool:tool=>tools.push(tool),setActiveTools:names=>active=names});
hooks.session_start();assert.deepEqual(active,['nexus_lookup']);assert.equal(tools.length,1);
assert.equal(tools[0].exposure,'model-only');assert.equal(tools[0].executionMode,'sequential');
for(const event of [{toolName:'shell',toolCallId:'call'},{toolName:'nexus_lookup',toolCallId:'call/1'},{toolName:'nexus_lookup',toolCallId:'call',parentToolCallId:'parent'}])assert.equal(hooks.tool_call(event).block,true);
assert.equal(hooks.tool_call({toolName:'nexus_lookup',toolCallId:'call'}),undefined);
let calls=0;
globalThis.fetch=async(url,request)=>{calls++;assert.equal(url,'http://127.0.0.1:1234/v1/tool');assert.deepEqual(JSON.parse(request.body),{call_id:'host-call'});assert.equal(request.redirect,'error');assert.ok(request.signal);return new Response(JSON.stringify({content:'host result',failed:true,end_tool_use:false}),{headers:{'content-type':'application/json'}})};
let result=await tools[0].execute('host-call',{path:'forged',command:'never run'});assert.equal(result.isError,true);assert.equal(result.content[0].text,'host result');assert.equal(calls,1);
await assert.rejects(()=>tools[0].execute('parent/1',{}),/NexusRouter tool unavailable/);assert.equal(calls,1);
globalThis.fetch=async()=>new Response(JSON.stringify({content:'done',failed:false,end_tool_use:true}),{headers:{'content-type':'application/json'}});
result=await tools[0].execute('host-call',{});assert.deepEqual(active,[]);assert.equal(result.isError,false);
for(const response of [new Response('sensitive upstream error',{status:403}),new Response(JSON.stringify({content:'x',failed:false,end_tool_use:false,extra:'no'}),{headers:{'content-type':'application/json'}})]){
 globalThis.fetch=async()=>response;await assert.rejects(()=>tools[0].execute('host-call',{}),e=>e.message==='NexusRouter tool unavailable; inspect the host journal');
}
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, e := exec.CommandContext(ctx, node, "--input-type=module", "-e", script, path).CombinedOutput()
	if e != nil {
		t.Fatalf("extension contract: %v %s", e, output)
	}
}
