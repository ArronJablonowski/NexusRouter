package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestChatRoutingConstraintsReachService(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		s := services()
		calls := 0
		run := func(_ context.Context, r app.Request) (app.Result, error) {
			calls++
			if r.HarnessID != "auto" || r.ModelID != "auto" || r.Domain != "writing" || r.Profile != "rubric-v1" || r.HarnessDifficulty != "hard" || r.ContextTokens != 32768 || r.MaxCost != 0.25 || !r.LocalRequired || len(r.Capabilities) != 1 || r.Capabilities[0] != "chat" || r.Messages[0].Content != "task" {
				t.Fatal("lost constraints", r)
			}
			return app.Result{Text: "answer", FinishReason: "stop"}, nil
		}
		s.Run = run
		s.RunTextStream = func(ctx context.Context, r app.Request, emit func(string) error) (app.Result, error) {
			v, e := run(ctx, r)
			if e == nil {
				e = emit(v.Text)
			}
			return v, e
		}
		h, err := New(token, 1, s)
		if err != nil {
			t.Fatal(err)
		}
		body := `{"model":"auto","harness_id":"auto","messages":[{"role":"user","content":"task"}],"routing":{"domain":"writing","profile":"rubric-v1","difficulty":"hard","context_tokens":32768,"max_cost":0.25,"local_required":true,"capabilities":["chat"]}`
		if streaming {
			body += `,"stream":true`
		}
		body += "}"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/chat/completions", body))
		if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "answer") {
			t.Fatal(w.Code, w.Body.String(), calls)
		}
	}
}

func TestChatRoutingRejectsInvalidMetadata(t *testing.T) {
	for _, routing := range []string{`null`, `[]`, `{"Domain":"writing"}`, `{"domain":"a","domain":"b"}`, `{"domain":" "}`, `{"profile":null}`, `{"context_tokens":null}`, `{"context_tokens":1.5}`, `{"context_tokens":0}`, `{"context_tokens":16777217}`, `{"max_cost":-1}`, `{"max_cost":1e999}`, `{"max_cost":null}`, `{"local_required":null}`, `{"local_required":"false"}`, `{"capabilities":null}`, `{"capabilities":["chat","chat"]}`, `{"capabilities":["x y"]}`, `{"executable":"/tmp/evil"}`, `{"evidence_dir":"/tmp/evil"}`} {
		_, _, err := decodeChatRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"task"}],"routing":` + routing + `}`))
		if err == nil {
			t.Fatal("accepted", routing)
		}
	}
	for _, extra := range []string{``, `,"routing":{}`, `,"routing":{"domain":"writing","profile":"p","context_tokens":4096}`} {
		_, _, err := decodeChatRequest([]byte(`{"model":"auto","harness_id":"auto","messages":[{"role":"user","content":"task"}]` + extra + `}`))
		if err == nil {
			t.Fatal("auto requires task class/context", extra)
		}
	}
}

func TestChatAutoHarnessWithEmptyOperatorEvidenceDoesNotDispatch(t *testing.T) {
	cfg := config.Defaults()
	cfg.Tools.Enabled = false
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	svc, err := app.NewService(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc.ConfigureHarnessEvidence(store)
	s := services()
	s.Run = svc.Run
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/chat/completions", `{"model":"auto","harness_id":"auto","messages":[{"role":"user","content":"task"}],"routing":{"domain":"writing","profile":"rubric-v1","context_tokens":8192,"max_cost":0}}`))
	if w.Code != 422 || !strings.Contains(w.Body.String(), "admission_denied") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestHarnessDifficultyDecoders(t *testing.T) {
	for _, d := range []string{"easy", "medium", "hard", "unknown"} {
		r, e := decodeRequest(strings.NewReader(`{"model_id":"chat","harness_id":"pi-local","harness_difficulty":"` + d + `","prompt":"fixture"}`))
		if e != nil || r.HarnessDifficulty != d {
			t.Fatal(r, e)
		}
	}
	for _, d := range []string{`null`, `""`, `"HARD"`, `" hard"`, `1`, `"impossible"`} {
		if _, e := decodeRequest(strings.NewReader(`{"model_id":"chat","harness_id":"pi-local","harness_difficulty":` + d + `,"prompt":"fixture"}`)); e == nil {
			t.Fatal("native accepted", d)
		}
		if _, _, e := decodeChatRequest([]byte(`{"model":"chat","harness_id":"pi-local","messages":[{"role":"user","content":"x"}],"routing":{"difficulty":` + d + `}}`)); e == nil {
			t.Fatal("chat accepted", d)
		}
	}
	if _, _, e := decodeChatRequest([]byte(`{"model":"chat","messages":[{"role":"user","content":"x"}],"routing":{"difficulty":"hard"}}`)); e == nil {
		t.Fatal("unconsumed difficulty accepted")
	}
}
