package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaThinkingExplicitAndDefaultWire(t *testing.T) {
	for _, mode := range []string{"default", "false", "true"} {
		t.Run(mode, func(t *testing.T) {
			var setting *bool
			if mode != "default" {
				v := mode == "true"
				setting = &v
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad request")
				}
				raw, exists := body["think"]
				if exists != (mode != "default") || exists && string(raw) != mode {
					t.Errorf("think=%s present=%v want=%s", raw, exists, mode)
				}
				fmt.Fprintln(w, `{"message":{"thinking":"private reasoning","content":"final answer"},"done":false}`)
				fmt.Fprintln(w, `{"message":{"content":""},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":2}`)
			}))
			defer server.Close()
			adapter, e := Build(context.Background(), nil, Connection{Version: 1, ID: "fixture", Kind: "ollama", Endpoint: server.URL, Transport: server.Client().Transport, OllamaThink: setting})
			if e != nil {
				t.Fatal(e)
			}
			// An adapter owns the copied setting; caller mutation must not change wire behavior.
			if setting != nil {
				*setting = !*setting
			}
			var text string
			e = adapter.Stream(context.Background(), request(), func(c Chunk) error { text += c.Text; return nil })
			if e != nil || text != "final answer" {
				t.Fatal(e, text)
			}
		})
	}
	value := false
	if _, e := Build(context.Background(), nil, Connection{Version: 1, ID: "fixture", Kind: "openai_compatible", Endpoint: "http://127.0.0.1:1", Transport: http.DefaultTransport, OllamaThink: &value}); e == nil {
		t.Fatal("Ollama setting accepted for different protocol")
	}
}
