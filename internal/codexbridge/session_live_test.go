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

// Separately opted-in real inference diagnostic. It never executes a proposed
// tool or answers its RPC; closing the session abandons any pending proposal.
func TestLiveCodexProposalProtocol(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_INFERENCE") != "1" {
		t.Skip("explicit supervised inference only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("CLI unavailable")
	}
	env := []string{}
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "CODEX_HOME"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var lastMethod string
	var knownFeatures []string
	s, err := launchChecked(ctx, LaunchSpec{Executable: bin, CWD: t.TempDir(), Model: "gpt-5.6-sol", Mode: "hybrid", Privacy: "cloud_allowed", Env: env}, func(ctx context.Context, spec codexrpc.ProcessSpec) (Wire, error) {
		p, err := codexrpc.StartProcess(ctx, spec)
		if err != nil {
			return nil, err
		}
		return &liveDiagnosticWire{Wire: p, last: &lastMethod, features: &knownFeatures}, nil
	}, func(ctx context.Context, spec codexrpc.ProcessSpec, args ...string) ([]byte, error) {
		b, err := launchMetadata(ctx, spec, args...)
		if len(args) == 2 && args[0] == "features" {
			knownFeatures, _, _ = launchProfile([]byte("codex-cli 0.153.4"), b)
		}
		return b, err
	})
	if err != nil {
		t.Fatal("launch failed")
	}
	defer s.Close()
	proposals := 0
	answerBytes := 0
	err = s.Stream(ctx, providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "Protocol smoke test. Call darwin.delegate exactly once with prompt Write a Go function returning 42 and validation go_source. Do not call any other tool."}}, Tools: []providers.Tool{{Name: "delegate", Description: "Request bounded local work", Parameters: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"validation":{"type":"string"}},"required":["prompt","validation"],"additionalProperties":false}`)}}}, func(c providers.Chunk) error {
		if c.ToolCall != nil {
			proposals++
		}
		answerBytes += len(c.Text)
		return nil
	})
	t.Logf("protocol metadata: prepared=%t thread=%t turn=%t frames=%d proposals=%d", s.prepared, s.thread != "", s.turn != "", s.frames, proposals)
	t.Logf("last frame classification: %s", lastMethod)
	t.Logf("answer bytes observed: %d", answerBytes)
	if err != nil {
		t.Fatal("live protocol failed; payload withheld")
	}
	if proposals != 1 {
		t.Fatal("expected one unexecuted proposal")
	}
}

type liveDiagnosticWire struct {
	Wire
	last     *string
	features *[]string
}

func (w *liveDiagnosticWire) Read() (codexrpc.Envelope, error) {
	e, err := w.Wire.Read()
	classification := "outside diagnostic allowlist"
	if e.Method == "" {
		classification = "RPC response"
	}
	if strings.HasPrefix(e.Method, "codex/event/") && len(e.Method) < 80 {
		classification = "legacy codex event"
	}
	switch e.Method {
	case "item/tool/call", "account/rateLimits/updated", "turn/moderationMetadata", "model/safetyBuffering/updated", "configWarning", "guardianWarning", "turn/plan/updated", "turn/diff/updated", "thread/name/updated", "serverRequest/resolved":
		classification = e.Method
	case "thread/started", "turn/started", "turn/completed", "item/started", "item/completed", "error", "warning", "deprecationNotice", "model/verification", "model/rerouted", "modelProvider/authRecoveryStarted", "modelProvider/authRecoveryCompleted", "remoteControl/status/changed", "thread/settings/updated", "thread/status/changed", "thread/tokenUsage/updated", "item/agentMessage/delta", "item/reasoning/summaryTextDelta":
		classification = e.Method
	}
	if e.Method == "warning" || e.Method == "deprecationNotice" {
		var note struct {
			Message string `json:"message"`
			Summary string `json:"summary"`
			Details string `json:"details"`
		}
		if json.Unmarshal(e.Params, &note) == nil {
			mentioned := []string{}
			for _, name := range *w.features {
				if strings.Contains(note.Message+note.Summary+note.Details, name) {
					mentioned = append(mentioned, name)
				}
			}
			classification += "[known feature mentions: " + strings.Join(mentioned, "|") + "]"
		}
	}
	if len(*w.last)+len(classification)+2 <= 16<<10 {
		*w.last += classification + ", "
	}
	return e, err
}
