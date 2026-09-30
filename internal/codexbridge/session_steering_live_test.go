package codexbridge

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// Explicit supervised opt-in only. Each case owns one checked CLI process and
// a synthetic conversation; no user history, local worker or tool is executed.
// A successful marker is narrow protocol evidence, not general obedience proof.
func TestLiveCodexSteeringBoundaries(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_STEERING") != "1" {
		t.Skip("explicit supervised steering inference only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("CLI unavailable")
	}
	var env []string
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "CODEX_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for _, paused := range []bool{false, true} {
		name := "completed_segment"
		if paused {
			name = "paused_synthetic_tool"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			var last string
			var features []string
			wire := &steeringLiveWire{}
			s, err := launchChecked(ctx, LaunchSpec{Executable: bin, CWD: t.TempDir(), Model: "gpt-5.6-sol", Mode: "hybrid", Privacy: "cloud_allowed", Env: env}, func(ctx context.Context, spec codexrpc.ProcessSpec) (Wire, error) {
				process, err := codexrpc.StartProcess(ctx, spec)
				if err != nil {
					return nil, err
				}
				wire.Wire = &liveDiagnosticWire{Wire: process, last: &last, features: &features}
				return wire, nil
			}, func(ctx context.Context, spec codexrpc.ProcessSpec, args ...string) ([]byte, error) {
				data, err := launchMetadata(ctx, spec, args...)
				if len(args) == 2 && args[0] == "features" {
					features, _, _ = launchProfile([]byte("codex-cli 0.153.4"), data)
				}
				return data, err
			})
			if err != nil {
				t.Fatal("checked launch failed")
			}
			defer s.Close()
			const firstMarker = "steering-first-7314"
			const secondMarker = "steering-revised-9265"
			req := providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "Reply only with the exact text " + firstMarker + ". Do not call tools."}}}
			if paused {
				req.Messages[0].Content = "Protocol smoke test. Call darwin.delegate exactly once with prompt 'read fixture marker' and validation 'text'. Do not call any other tool. After the tool reply, wait for and follow updated user guidance."
				req.Tools = []providers.Tool{{Name: "delegate", Description: "Read an inert host test fixture; no external action or actual worker runs.", Parameters: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"validation":{"type":"string","enum":["text"]}},"required":["prompt","validation"],"additionalProperties":false}`)}}
			}
			var initial strings.Builder
			var calls []providers.ToolCall
			firstFinish := ""
			err = s.Stream(ctx, req, func(c providers.Chunk) error {
				initial.WriteString(c.Text)
				if c.ToolCall != nil {
					own := *c.ToolCall
					own.Arguments = append(json.RawMessage(nil), own.Arguments...)
					calls = append(calls, own)
				}
				if c.Done {
					firstFinish = c.FinishReason
				}
				return nil
			})
			if err != nil {
				t.Logf("first boundary metadata: last=%s text_bytes=%d proposals=%d", last, initial.Len(), len(calls))
				t.Fatal("live first boundary failed; payload withheld")
			}
			if paused {
				if firstFinish != "tool_calls" || len(calls) != 1 || calls[0].Name != "delegate" || wire.replies != 0 {
					t.Fatal("synthetic tool was not exactly one unanswered proposal")
				}
			} else if firstFinish != "stop" || len(calls) != 0 || strings.TrimSpace(initial.String()) != firstMarker {
				t.Fatal("initial marker mismatch; payload withheld")
			}
			// Preserve the exact emitted assistant segment and tool call. The tool
			// result is host-authored test data, not a claim of tool execution.
			next := req
			next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", Content: initial.String(), ToolCalls: calls})
			if paused {
				next.Messages = append(next.Messages, providers.Message{Role: "tool", ToolCallID: calls[0].ID, Content: `{"fixture":"synthetic-read-only-result","executed":false}`})
			}
			next.Messages = append(next.Messages, providers.Message{Role: "user", Content: "Updated guidance: do not call any tool or repeat work. Reply only with the exact text " + secondMarker + "."})
			var answer strings.Builder
			proposals, completed := 0, false
			err = s.Stream(ctx, next, func(c providers.Chunk) error {
				answer.WriteString(c.Text)
				if c.ToolCall != nil {
					proposals++
				}
				if c.Done && c.FinishReason == "stop" {
					completed = true
				}
				return nil
			})
			t.Logf("steering protocol metadata: paused=%t threads=%d imports=%d turns=%d steers=%d replies=%d completed=%t proposals=%d answer_bytes=%d", paused, wire.threads, wire.imports, wire.turns, wire.steers, wire.replies, completed, proposals, answer.Len())
			if err != nil {
				t.Logf("last frame classification: %s", last)
				t.Fatal("live steering protocol failed; payload withheld")
			}
			if !completed || proposals != 0 || strings.TrimSpace(answer.String()) != secondMarker || wire.threads != 1 || wire.imports != 0 {
				t.Fatal("steering result mismatch; payload withheld")
			}
			if paused {
				if wire.turns != 1 || wire.steers != 1 || wire.replies != 1 {
					t.Fatal("paused tool control or response duplicated")
				}
			} else if wire.turns != 2 || wire.steers != 0 || wire.replies != 0 {
				t.Fatal("completed-segment control mismatch")
			}
		})
	}
}

type steeringLiveWire struct {
	Wire
	threads, imports, turns, steers, replies int
}

func (w *steeringLiveWire) Write(e codexrpc.Envelope) error {
	switch e.Method {
	case "thread/start":
		w.threads++
	case "thread/inject_items":
		w.imports++
	case "turn/start":
		w.turns++
	case "turn/steer":
		w.steers++
	case "":
		if len(e.ID) > 0 && len(e.Result) > 0 {
			w.replies++
		}
	}
	return w.Wire.Write(e)
}
