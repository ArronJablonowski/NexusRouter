package hermes

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const initLine = `{"type":"system","subtype":"init","model":"fixture","session_id":"session-1","timestamp":100}` + "\n"
const resultLine = `{"type":"result","session_id":"session-1","exit_code":0,"text":"answer","tokens":{"input":20,"output":3,"total":23,"cache_read":0,"cache_write":0},"duration_ms":10,"timestamp":110}` + "\n"
const textLine = `{"type":"text","text":"answer","timestamp":105}` + "\n"

func TestProjectionProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"streaming", initLine + textLine + resultLine, true},
		{"nonstreaming", initLine + resultLine, true},
		{"late-session", strings.Replace(initLine, `"session-1"`, `""`, 1) + resultLine, true},
		{"CRLF", strings.ReplaceAll(initLine+resultLine, "\n", "\r\n"), true},
		{"no-init", resultLine, false},
		{"no-terminal", initLine + textLine, false},
		{"duplicate-init", initLine + initLine + resultLine, false},
		{"duplicate-terminal", initLine + resultLine + resultLine, false},
		{"wrong-model", strings.Replace(initLine, `"fixture"`, `"other"`, 1) + resultLine, false},
		{"session-mismatch", initLine + strings.Replace(resultLine, `"session-1"`, `"other"`, 1), false},
		{"nonzero-exit", initLine + strings.Replace(resultLine, `"exit_code":0`, `"exit_code":1`, 1), false},
		{"error", initLine + strings.Replace(resultLine, `"exit_code":0`, `"exit_code":0,"error":"failed"`, 1), false},
		{"empty-delta-mismatch", initLine + strings.Replace(textLine, `"answer"`, `""`, 1) + resultLine, false},
		{"delta-mismatch", initLine + textLine + strings.Replace(resultLine, `"text":"answer"`, `"text":"other"`, 1), false},
		{"tool-start", initLine + `{"type":"tool_use","name":"terminal","timestamp":101}` + "\n" + resultLine, false},
		{"tool-result", initLine + `{"type":"tool_result","name":"terminal","timestamp":101}` + "\n" + resultLine, false},
		{"after-terminal", initLine + resultLine + textLine, false},
		{"missing-newline", strings.TrimSuffix(initLine+resultLine, "\n"), false},
		{"negative-count", initLine + strings.Replace(resultLine, `"input":20`, `"input":-1`, 1), false},
		{"missing-count", initLine + strings.Replace(resultLine, `"input":20,`, ``, 1), false},
		{"duplicate-key", initLine + strings.Replace(resultLine, `"exit_code":0`, `"exit_code":1,"EXIT_CODE":0`, 1), false},
		{"oversized-line", initLine + strings.Repeat("x", MaxRecordBytes+1) + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projection, err := ParseProjection([]byte(tc.body), 0, "fixture")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid && projection.Text != "answer" {
				t.Fatal("lost output")
			}
			if !tc.valid && projection != (Projection{}) {
				t.Fatal("partial output escaped")
			}
		})
	}
	for _, exit := range []int{-1, 1, 2, 130, 137} {
		if _, err := ParseProjection([]byte(initLine+resultLine), exit, "fixture"); err == nil {
			t.Fatal("trusted JSON over process exit")
		}
	}
}

// Runs only the installed emitter module's pure callbacks. No agent, provider,
// credential lookup, user configuration or Hermes gateway is started.
func TestInstalledEmitter(t *testing.T) {
	if os.Getenv("NEXUS_HERMES_PROJECTION") != "1" {
		t.Skip("installed Hermes emitter qualification is opt-in")
	}
	root := "/Users/aj_lobster/.hermes/hermes-agent"
	revision, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(revision)) != SupportedRevision {
		t.Fatal("installed Hermes revision changed")
	}
	for _, mode := range []string{"success", "length", "tool", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			script := `import runpy,sys
Emitter=runpy.run_path(sys.argv[1])["StreamJsonEmitter"]
emitter=Emitter(model="fixture",session_id="session-1")
emitter.on_text_delta("answer")
data={"final_response":"answer","input_tokens":20,"output_tokens":3,"total_tokens":23}
if sys.argv[2]=="length":
 data["finish_reason"]="length"
 data["provider"]="another-provider"
if sys.argv[2]=="tool": emitter.on_tool_progress("tool.started",tool_name="terminal",args={"command":"fixture only"})
emitter.emit_result(data)
if sys.argv[2]=="duplicate": emitter.emit_result(data)
`
			body, err := exec.Command("/Users/aj_lobster/.hermes/tools/python-3.14.7+20260901-darwin-arm64/bin/python3", "-I", "-c", script, root+"/hermes_cli/stream_json.py", mode).Output()
			if err != nil {
				t.Fatal("emitter failed", err)
			}
			projection, err := ParseProjection(body, 0, "fixture")
			valid := mode == "success" || mode == "length"
			if (err == nil) != valid {
				t.Fatal("installed emitter contract mismatch", err)
			}
			if valid && projection.Text != "answer" {
				t.Fatal("lost emitted text")
			}
			// Even a length/other-provider input yields this projection. The
			// future runner must supply verified upstream completion evidence.
		})
	}
}
