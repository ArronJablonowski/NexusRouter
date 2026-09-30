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

// Explicit opt-in uses the existing signed-in CLI for one Sol inference. The
// history is synthetic, not a user session, and the active Darwin tool catalog
// is empty. No old tool is executed or answered during the import.
func TestLiveCodexImportedHistory(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_HISTORY") != "1" {
		t.Skip("explicit supervised history inference only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("CLI unavailable")
	}
	env := []string{}
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "CODEX_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var lastMethod string
	var features []string
	var imports, turns int
	s, err := launchChecked(ctx, LaunchSpec{Executable: bin, CWD: t.TempDir(), Model: "gpt-5.6-sol", Mode: "hybrid", Privacy: "cloud_allowed", Env: env}, func(ctx context.Context, spec codexrpc.ProcessSpec) (Wire, error) {
		process, err := codexrpc.StartProcess(ctx, spec)
		if err != nil {
			return nil, err
		}
		return &historyLiveWire{Wire: &liveDiagnosticWire{Wire: process, last: &lastMethod, features: &features}, imports: &imports, turns: &turns}, nil
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
	const marker = "history-check-7314"
	request := providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{
		{Role: "user", Content: "Ask a worker for the test marker."},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "historical-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"return the test marker","validation":"text"}`)}}},
		{Role: "tool", ToolCallID: "historical-call", Content: marker},
		{Role: "user", Content: "The previous tool call is finished; do not call any tool or repeat work. Reply with only the exact marker from its saved output."},
	}}
	var answer strings.Builder
	proposals, completed := 0, false
	err = s.Stream(ctx, request, func(chunk providers.Chunk) error {
		answer.WriteString(chunk.Text)
		if chunk.ToolCall != nil {
			proposals++
		}
		if chunk.Done && chunk.FinishReason == "stop" {
			completed = true
		}
		return nil
	})
	t.Logf("history protocol metadata: imports=%d turns=%d completed=%t proposals=%d answer_bytes=%d", imports, turns, completed, proposals, answer.Len())
	if err != nil {
		t.Logf("last frame classification: %s", lastMethod)
		t.Fatal("live history protocol failed; payload withheld")
	}
	if imports != 1 || turns != 1 || proposals != 0 || !completed || strings.TrimSpace(answer.String()) != marker {
		t.Fatal("history was not faithfully consumed; payload withheld")
	}
}

type historyLiveWire struct {
	Wire
	imports, turns *int
}

func (w *historyLiveWire) Write(event codexrpc.Envelope) error {
	if event.Method == "thread/inject_items" {
		*w.imports++
	}
	if event.Method == "turn/start" {
		*w.turns++
	}
	return w.Wire.Write(event)
}
