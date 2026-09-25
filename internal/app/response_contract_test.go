package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
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
	if err != nil || len(messages) != 2 || messages[0].Content != before || messages[1].Role != "user" || !strings.Contains(messages[1].Content, "JSON") {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	again, err := prepareTaskContext(context.Background(), &r, nil)
	if err != nil || len(again) != 2 {
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
