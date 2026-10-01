package textgateway

import (
	"os"
	"strings"
	"testing"
)

// Ollama 0.34.4 emits this terminal metric on native tool responses.
// Cached tokens are already included in prompt_eval_count, not extra input.
func TestOllamaAgentCachedPromptMetric(t *testing.T) {
	for _, n := range []string{"0", "7", "10"} {
		body := strings.Replace(nativeAgentCall, `"prompt_eval_count":10`, `"prompt_eval_count":10,"prompt_eval_cached_count":`+n, 1)
		got, err := ollamaAgentCompletion(strings.NewReader(body), "model")
		if err != nil || len(got.Calls) != 1 || got.Usage == nil || got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 3 {
			t.Fatalf("cached=%s: %+v %v", n, got, err)
		}
	}
	for _, n := range []string{"null", "-1", "11", "1.5", `"0"`, "true", "1099511627777"} {
		body := strings.Replace(nativeAgentCall, `"prompt_eval_count":10`, `"prompt_eval_count":10,"prompt_eval_cached_count":`+n, 1)
		got, err := ollamaAgentCompletion(strings.NewReader(body), "model")
		if err == nil || len(got.Calls) != 0 || got.Usage != nil {
			t.Fatalf("invalid cached=%s escaped: %+v", n, got)
		}
	}
	for _, body := range []string{
		strings.Replace(nativeAgentCall, `"done":false`, `"done":false,"prompt_eval_cached_count":0`, 1),
		strings.Replace(nativeAgentCall, `"prompt_eval_count":10,"eval_count":3`, `"prompt_eval_cached_count":0`, 1),
	} {
		if got, err := ollamaAgentCompletion(strings.NewReader(body), "model"); err == nil || len(got.Calls) != 0 {
			t.Fatal("unbound metric accepted", got)
		}
	}
}

func TestOllamaAgentCapturedNativeResponse(t *testing.T) {
	body, err := os.ReadFile("testdata/ollama-0.34.4-tool-call.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ollamaAgentCompletion(strings.NewReader(string(body)), "devstral-small-2:24b-instruct-2512-q4_K_M")
	if err != nil || len(got.Calls) != 1 || got.Calls[0].Name != "read_file" || string(got.Calls[0].Arguments) != `{"path":"case-1.json"}` || got.Usage == nil || got.Usage.InputTokens != 705 || got.Usage.OutputTokens != 14 {
		t.Fatal(got, err)
	}
}
