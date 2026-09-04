package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestDiscoveryRequiresProviderList(t *testing.T) {
	for _, kind := range []string{"ollama", "openai_compatible"} {
		field, identity := "models", "name"
		if kind == "openai_compatible" {
			field, identity = "data", "id"
		}
		for _, body := range []string{"null", `{}`, `{"error":"fixture-secret"}`, fmt.Sprintf(`{"%s":null}`, field), fmt.Sprintf(`{"%s":[{}]}`, field), fmt.Sprintf(`{"%s":[{"%s":3}]}`, field, identity), fmt.Sprintf(`{"%s":[{"%s":" "}]}`, field, identity)} {
			t.Run(kind+body, func(t *testing.T) {
				p := fixtureProvider(t, kind, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
				models, err := p.Models(context.Background())
				var failure *Failure
				if !errors.As(err, &failure) || failure.Code != "invalid_response" || models != nil || strings.Contains(err.Error(), "secret") {
					t.Fatalf("invalid discovery accepted: %v, %v", models, err)
				}
			})
		}
		for _, empty := range []bool{false, true} {
			p := fixtureProvider(t, kind, func(w http.ResponseWriter, _ *http.Request) {
				if empty {
					fmt.Fprintf(w, `{"%s":[]}`, field)
				} else {
					fmt.Fprintf(w, `{"%s":[{"%s":"a"},{"%s":"a"},{"%s":"b"}]}`, field, identity, identity, identity)
				}
			})
			models, err := p.Models(context.Background())
			if err != nil || (empty && len(models) != 0) || (!empty && strings.Join(models, ",") != "a,b") {
				t.Fatalf("valid discovery rejected: %v, %v", models, err)
			}
		}
	}
}

func TestDiscoveryCancellationDuringBody(t *testing.T) {
	started := make(chan struct{})
	p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[`)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-started; cancel() }()
	if _, err := p.Models(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestMalformedSSEDoesNotReleaseTools(t *testing.T) {
	for name, payload := range map[string]string{
		"refusal":           `{"choices":[{"delta":{"refusal":"fixture-secret"},"finish_reason":"stop"}]}`,
		"duplicate choices": `{"choices":[{"delta":{}},{"delta":{},"finish_reason":"stop"}]}`,
		"unsupported tool":  `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"x","type":"custom","function":{"name":"x","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		"invalid UTF8":      "{\"choices\":[{\"delta\":{\"content\":\"\xff\"},\"finish_reason\":\"stop\"}]}",
	} {
		t.Run(name, func(t *testing.T) {
			err := readSSE(strings.NewReader("data: "+payload+"\n\ndata: [DONE]\n\n"), func(Chunk) error { t.Error("invalid response released"); return nil })
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe failure: %v", err)
			}
		})
	}
}

func TestDuplicateUsageRejected(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		strings.Repeat("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\n", 2) + "data: [DONE]\n\n"
	usage := 0
	err := readSSE(strings.NewReader(body), func(c Chunk) error {
		if c.Done {
			t.Error("duplicate usage accepted")
		}
		if c.Usage != nil {
			usage++
		}
		return nil
	})
	if err == nil || usage != 1 {
		t.Fatalf("usage=%d err=%v", usage, err)
	}
}

func TestOllamaToolBatchLimit(t *testing.T) {
	call := `{"message":{"tool_calls":[{"function":{"name":"lookup","arguments":{}}}]},"done":false}` + "\n"
	for _, n := range []int{128, 129} {
		calls, done := 0, false
		err := readOllama(strings.NewReader(strings.Repeat(call, n)+"{\"done\":true,\"done_reason\":\"stop\"}\n"), func(c Chunk) error {
			if c.ToolCall != nil {
				calls++
			}
			done = done || c.Done
			return nil
		})
		if n == 128 && (err != nil || calls != n || !done) {
			t.Fatalf("valid batch: %d %v %v", calls, done, err)
		}
		if n == 129 && (err == nil || calls != 0 || done) {
			t.Fatalf("oversized batch: %d %v %v", calls, done, err)
		}
	}
}

func TestSSEMultilineUnicodeAndCRLF(t *testing.T) {
	body := ": heartbeat\r\ndata: {\"choices\":[{\"index\":0,\r\ndata: \"delta\":{\"content\":\"日本語 🦎\"},\"finish_reason\":\"stop\"}]}\r\n\r\ndata: [DONE]\r\n\r\n"
	var text string
	var done bool
	if err := readSSE(strings.NewReader(body), func(c Chunk) error { text += c.Text; done = done || c.Done; return nil }); err != nil || text != "日本語 🦎" || !done {
		t.Fatalf("multiline SSE: %q %v %v", text, done, err)
	}
}

func TestAmbiguousToolCatalogNeverSent(t *testing.T) {
	p := fixtureProvider(t, "openai_compatible", func(http.ResponseWriter, *http.Request) {
		t.Error("ambiguous tool catalog sent")
	})
	for _, count := range []int{2, 129} {
		r := request()
		for i := range count {
			name := "same"
			if count == 129 {
				name = fmt.Sprintf("tool_%d", i)
			}
			r.Tools = append(r.Tools, Tool{Name: name, Parameters: json.RawMessage(`{"type":"object"}`)})
		}
		var failure *Failure
		if err := p.Stream(context.Background(), r, func(Chunk) error { return nil }); !errors.As(err, &failure) || failure.Code != "invalid_tool_schema" {
			t.Fatalf("ambiguous catalog accepted: %v", err)
		}
	}
}
