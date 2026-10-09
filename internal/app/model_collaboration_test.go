package app

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/internal/agentchat"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"strings"
	"testing"
)

type collaborationFixtureProvider struct{ t *testing.T }

func (p collaborationFixtureProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}
func (p collaborationFixtureProvider) Stream(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	last := r.Messages[len(r.Messages)-1]
	if last.Role == "tool" {
		if r.Model == "z" && last.ToolCallID == "message" {
			reply := providers.ToolCall{ID: "reply", Name: "collaboration_send", Arguments: json.RawMessage(`{"recipient":"a","topic":"parser","text":"parser idea reply"}`)}
			if e := emit(providers.Chunk{ToolCall: &reply}); e != nil {
				return e
			}
			return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
		}
		if !strings.Contains(last.Content, "parser idea") {
			p.t.Fatal("knowledge not returned", last)
		}
		if e := emit(providers.Chunk{Text: "done"}); e != nil {
			return e
		}
		return emit(providers.Chunk{Done: true, FinishReason: "stop"})
	}
	found := false
	for _, spec := range r.Tools {
		if spec.Name == "collaboration_send" {
			found = true
		}
	}
	if !found {
		p.t.Fatal("collaboration not advertised")
	}
	call := providers.ToolCall{ID: "message", Name: "collaboration_send", Arguments: json.RawMessage(`{"recipient":"z","topic":"parser","text":"parser idea"}`)}
	if r.Model == "z" || last.Content == "read replies" {
		call.Name = "collaboration_read"
		call.Arguments = json.RawMessage(`{"topic":"parser"}`)
	}
	if e := emit(providers.Chunk{ToolCall: &call}); e != nil {
		return e
	}
	return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
}

type collaborationFixtureFactory struct{ t *testing.T }

func (f collaborationFixtureFactory) Build(context.Context, providers.Connection) (providers.Provider, error) {
	return collaborationFixtureProvider{f.t}, nil
}
func TestModelCollaborationAcrossRealTaskLoops(t *testing.T) {
	s, _ := autoFixture(t)
	s.settings.Tools.CollaborationEnabled = true
	s.providerFactory = collaborationFixtureFactory{t}
	a, e := s.Run(context.Background(), Request{ModelID: "a", Prompt: "leave an idea"})
	if e != nil {
		t.Fatal(a, e)
	}
	b, e := s.Run(context.Background(), Request{ModelID: "z", Prompt: "read ideas"})
	if e != nil || b.Text != "done" {
		t.Fatal(b, e)
	}
	c, e := s.Run(context.Background(), Request{ModelID: "a", Prompt: "read replies"})
	if e != nil || c.Text != "done" {
		t.Fatal(c, e)
	}
	page, e := s.BrowserCollaboration(context.Background(), webui.CollaborationOptions{Topic: "parser"})
	if e != nil || len(page.Messages) != 2 {
		t.Fatal(page, e)
	}
	m := page.Messages[1]
	if page.Messages[0].SenderID != "z" || page.Messages[0].TaskID != b.TaskID || page.Messages[0].Recipient != "a" {
		t.Fatal("reply lost its source", page)
	}
	if m.SenderID != "a" || m.SenderModel != "a" || m.Provider != "local" || m.TaskID != a.TaskID || m.Hostname == "" || m.Runner == "" || m.Harness == "" || m.SentAt.IsZero() {
		t.Fatal("missing trusted provenance", m)
	}
	s.settings.Tools.CollaborationEnabled = false
	page, e = s.BrowserCollaboration(context.Background(), webui.CollaborationOptions{})
	if e != nil || page.Enabled || len(page.Messages) != 2 {
		t.Fatal("disabled history lost", page, e)
	}
}
func TestModelCollaborationBoundaryRejectsForgeryAndCancellation(t *testing.T) {
	s, _ := autoFixture(t)
	store, e := agentchat.Open(context.Background(), s.settings.Telemetry.Database+".collaboration")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	sender, _ := collaborationSender(s.settings.Models[0], s.settings.Providers[0], Request{}, "task", true)
	sender.SessionID = "session"
	exec := &modelCollaborationExecutor{store: store, sender: sender, models: s.settings.Models, taskID: "task", sessionID: "session", secrets: []string{"fake-secret"}}
	x := runtime.ToolExecution{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt", Call: providers.ToolCall{ID: "call", Name: "collaboration_send", Arguments: json.RawMessage(`{"recipient":"z","topic":"topic","text":"fake-secret"}`)}}
	bad := x
	bad.TaskID = "forged"
	if _, e = exec.ExecuteScoped(context.Background(), bad); e == nil {
		t.Fatal("forged task accepted")
	}
	bad = x
	bad.Call.Arguments = json.RawMessage(`{"recipient":"z","topic":"x","text":"hi","sender_id":"forged"}`)
	if _, e = exec.ExecuteScoped(context.Background(), bad); e == nil {
		t.Fatal("forged provenance accepted")
	}
	bad.Call.Arguments = json.RawMessage(`{"recipient":"z","topic":"x","text":"hi","text":"changed"}`)
	if _, e = exec.ExecuteScoped(context.Background(), bad); e == nil {
		t.Fatal("duplicate fields accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = exec.ExecuteScoped(canceled, x); e == nil {
		t.Fatal("canceled send accepted")
	}
	out, e := exec.ExecuteScoped(context.Background(), x)
	if e != nil || out.Effect != runtime.ConfirmedEffect || strings.Contains(out.Content, "fake-secret") {
		t.Fatal(out, e)
	}
	out2, e := exec.ExecuteScoped(context.Background(), x)
	if e != nil || out2.Content != out.Content {
		t.Fatal("retry duplicated timestamp", out2, e)
	}
}
func TestCollaborationConfiguredHarnessProvenanceAndIsolation(t *testing.T) {
	s, _ := autoFixture(t)
	r := Request{nativeHarness: &NativeHarness{Kind: "pi", ID: "pi-coder"}}
	m, e := collaborationSender(s.settings.Models[0], s.settings.Providers[0], r, "task", true)
	if e != nil || m.Harness != "pi" || m.HarnessID != "pi-coder" || m.Runner != "Ollama" {
		t.Fatal(m, e)
	}
	s.settings.Tools.CollaborationEnabled = true
	stripped := remoteExecutionSettings(s.settings, Request{RemoteExecution: &runtime.RemoteExecution{Mode: "direct", Depth: 1}})
	if stripped.Tools.CollaborationEnabled {
		t.Fatal("remote execution retained ambient messaging")
	}
	native := nativeToolsFor(s.settings, nil)
	found := false
	for _, spec := range native.Catalog {
		found = found || spec.Name == "collaboration_send"
	}
	if !found {
		t.Fatal("native identity omitted collaboration catalogue")
	}
}
func TestCollaborationRunnerMetadataDoesNotGuessBackend(t *testing.T) {
	if collaborationRunner(config.Provider{Kind: "openai_compatible"}) != "OpenAI-compatible endpoint (upstream runner undeclared)" {
		t.Fatal("unknown upstream fabricated")
	}
	if collaborationRunner(config.Provider{Kind: "openai_compatible", Runner: "MLX-LM"}) != "MLX-LM (configured)" {
		t.Fatal("configured runner lost")
	}
}
