package openclaw

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func fixture() map[string]any {
	return map[string]any{"ok": true, "status": "ok", "final": "answer", "payloads": []any{map[string]any{"text": "answer"}}, "provider": "fixture", "model": "test-model", "sessionId": "session-1", "assistantTurns": 1, "codeModeEngaged": false}
}

func TestProjection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		good   bool
	}{
		{"success", func(e map[string]any) {}, true},
		{"metadata-fallback", func(e map[string]any) { e["payloads"] = []any{} }, true},
		{"additive", func(e map[string]any) { e["futureDiagnostic"] = "opaque" }, true},
		{"reasoning-excluded", func(e map[string]any) {
			e["payloads"] = []any{map[string]any{"text": "private", "isReasoning": true}, map[string]any{"text": "answer\ufeff"}}
		}, true},
		{"failure", func(e map[string]any) { e["ok"] = false }, false},
		{"timeout", func(e map[string]any) { e["status"] = "timeout" }, false},
		{"error", func(e map[string]any) { e["error"] = map[string]any{"message": "failure"} }, false},
		{"null-error", func(e map[string]any) { e["error"] = nil }, false},
		{"wrong-provider", func(e map[string]any) { e["provider"] = "fallback" }, false},
		{"wrong-model", func(e map[string]any) { e["model"] = "fallback" }, false},
		{"missing-turns", func(e map[string]any) { delete(e, "assistantTurns") }, false},
		{"extra-turn", func(e map[string]any) { e["assistantTurns"] = 2 }, false},
		{"code-mode", func(e map[string]any) { e["codeModeEngaged"] = true }, false},
		{"bridge", func(e map[string]any) { e["bridgeCalls"] = map[string]any{"search": 1} }, false},
		{"tools", func(e map[string]any) { e["toolSummary"] = map[string]any{"calls": 1} }, false},
		{"tools-with-zero-count", func(e map[string]any) { e["toolSummary"] = map[string]any{"calls": 0, "tools": []string{"exec"}} }, false},
		{"empty-session", func(e map[string]any) { e["sessionId"] = "" }, false},
		{"empty-output", func(e map[string]any) { e["final"] = "\n" }, false},
		{"mismatched-output", func(e map[string]any) { e["final"] = "another answer" }, false},
		{"media", func(e map[string]any) {
			e["payloads"] = []any{map[string]any{"mediaUrl": "https://example.invalid/image"}}
		}, false},
		{"error-payload", func(e map[string]any) { e["payloads"] = []any{map[string]any{"isError": true}} }, false},
		{"invalid-usage", func(e map[string]any) { e["usage"] = map[string]any{"input": -1} }, false},
		{"invalid-cost", func(e map[string]any) { e["costUsd"] = -1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fixture()
			tc.change(e)
			b, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			p, err := ParseProjection(b, 0, "fixture", "test-model")
			if tc.good {
				if err != nil || p.Text != "answer" {
					t.Fatalf("projection=%+v error=%v", p, err)
				}
			} else if !errors.Is(err, ErrProjection) || p != (Projection{}) {
				t.Fatalf("accepted unsupported envelope: %+v %v", p, err)
			}
		})
	}
}

func TestFramingAndProcessExit(t *testing.T) {
	b, _ := json.Marshal(fixture())
	for _, code := range []int{-1, 1, 2, 130, 137} {
		if _, err := ParseProjection(b, code, "fixture", "test-model"); err == nil {
			t.Fatalf("accepted exit %d", code)
		}
	}
	for _, bad := range [][]byte{
		append(append([]byte{}, b...), b...), []byte("log\n" + string(b)), []byte(`{"ok":false,"ok":true}`),
		[]byte(strings.Replace(string(b), `"ok":true`, `"ok":false,"OK":true`, 1)),
		[]byte(strings.Replace(string(b), `"ok":true`, `"ok":false,"\u006fk":true`, 1)),
		[]byte(strings.Repeat(" ", MaxEnvelopeBytes+1)), append([]byte{0xff}, b...),
	} {
		if _, err := ParseProjection(bad, 0, "fixture", "test-model"); err == nil {
			t.Fatal("accepted ambiguous or oversized stdout")
		}
	}
}

// Exercise the installed package's actual pure projection implementation without
// invoking an agent, loading operator configuration, or making inference calls.
// This deliberately does NOT qualify process isolation or native execution.
func TestInstalledProjection(t *testing.T) {
	if os.Getenv("NEXUS_OPENCLAW_PROJECTION") != "1" {
		t.Skip("installed projection qualification is opt-in")
	}
	script := `
const fs = require('fs');
const root = '/opt/homebrew/lib/node_modules/openclaw';
if (JSON.parse(fs.readFileSync(root+'/package.json')).version !== '2026.9.7') throw Error('version changed');
const dir = root+'/dist';
const files = fs.readdirSync(dir).filter(x => /^agent-exec-[^.]+\.mjs$/.test(x));
const sources = files.map(x=>fs.readFileSync(dir+'/'+x,'utf8')).filter(x=>x.includes('function classifyAgentExecResult('));
if (sources.length !== 1) throw Error('ambiguous installed projector');
const source = sources[0];
const start = source.indexOf('function projectAgentExecPayload(');
const end = source.indexOf('//#endregion',start);
if (start < 0 || end < 0) throw Error('projection boundary changed');
eval(source.slice(start,end));
const base = {payloads:[{text:'answer'}],meta:{agentMeta:{provider:'fixture',model:'test-model',sessionId:'session-1',assistantTurns:1}}};
const length = structuredClone(base); length.meta.stopReason = 'length';
process.stdout.write(JSON.stringify([classifyAgentExecResult(base),classifyAgentExecResult(length)]));
`
	b, err := exec.Command("node", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("installed projector failed: %v %s", err, b)
	}
	var envelopes []json.RawMessage
	if err := json.Unmarshal(b, &envelopes); err != nil || len(envelopes) != 2 {
		t.Fatalf("invalid fixture %s", b)
	}
	for _, envelope := range envelopes {
		if _, err := ParseProjection(envelope, 0, "fixture", "test-model"); err != nil {
			t.Fatal(err)
		}
	}
	// The projection cannot distinguish these. The future runner must bind a
	// separately validated gateway normal-stop response before committing output.
	if string(envelopes[0]) != string(envelopes[1]) {
		t.Fatal("installed stop-reason projection changed; revisit boundary")
	}
}
