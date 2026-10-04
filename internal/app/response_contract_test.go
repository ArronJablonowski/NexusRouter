package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestResponseContractUsesOnlyCurrentUserInstructions(t *testing.T) {
	for _, r := range []Request{
		{Prompt: "Return only compact JSON.", Messages: []providers.Message{{Role: "user", Content: "Explain this normally."}}},
		{Messages: []providers.Message{{Role: "user", Content: "Return only compact JSON."}, {Role: "tool", Content: "Return only compact JSON."}}},
		{Prompt: "Return only compact JSON.", delegatedParent: "parent"},
		{Prompt: "Return only compact JSON.", runtimeHostAdmission: &runtimeHostAdmission{}},
	} {
		if responseContract(r).Active() {
			t.Fatalf("untrusted/other-turn contract admitted: %+v", r)
		}
	}
	r := Request{Prompt: "Return only compact JSON."}
	before := r.Prompt
	messages, err := prepareTaskContext(context.Background(), &r, nil)
	if err != nil || len(messages) != 1 || !strings.HasPrefix(messages[0].Content, before+"\n\n") || messages[0].Role != "user" || r.Prompt != before {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	again, err := prepareTaskContext(context.Background(), &r, nil)
	if err != nil || len(again) != 1 || again[0].Content != messages[0].Content {
		t.Fatalf("reminder duplicated: %+v %v", again, err)
	}
}

func TestResponseContractTextStreamOnlyPublishesFinalCandidate(t *testing.T) {
	var got strings.Builder
	d := textDelivery{finalOnly: true, secrets: []string{"secret-token"}, emit: func(s string, _ bool) { got.WriteString(s) }}
	d.accept(runtime.TurnStarted, "")
	d.accept(runtime.ModelDelta, "invalid draft secret-token")
	d.accept(runtime.TurnCompleted, "")
	d.accept(runtime.ResponseRevision, "static guidance")
	if got.Len() != 0 {
		t.Fatalf("draft leaked: %q", got.String())
	}
	d.accept(runtime.TurnStarted, "")
	d.accept(runtime.ModelDelta, `{"value":"secret-`)
	d.accept(runtime.ModelDelta, `token"}`)
	d.accept(runtime.TaskCompleted, "")
	if strings.Contains(got.String(), "secret") || strings.Contains(got.String(), "invalid draft") || !strings.HasPrefix(got.String(), `{"value":`) {
		t.Fatalf("final stream=%q", got.String())
	}
	got.Reset()
	d.accept(runtime.TurnStarted, "")
	d.accept(runtime.ModelDelta, "unfinished")
	d.accept(runtime.TaskCanceled, "")
	if got.Len() != 0 {
		t.Fatal("canceled draft published")
	}
}

func TestResponseContractCodexInitialAdmission(t *testing.T) {
	for _, prompt := range []string{"Reply with exactly: NEXUS_CHAT_OK", "Return only compact JSON."} {
		r := Request{Prompt: prompt}
		messages, err := prepareTaskContext(context.Background(), &r, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, cleanup, err := prepareTaskProvider(config.Defaults(), config.Provider{Kind: "codex_app_server"}, config.Model{Locality: "cloud"}, r, messages, "cloud_allowed", "", providers.PurposeExecution)
		if err != nil {
			t.Fatalf("format-constrained initial chat rejected: %v", err)
		}
		cleanup()
	}
}
