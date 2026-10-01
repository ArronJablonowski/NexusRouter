package pi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOllamaTranslationPreservesLimitsAndRejectsUnsupportedControls(t *testing.T) {
	body := `{"model":"fixture","messages":[{"role":"system","content":"policy"},{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}],"stream":true,"max_tokens":1024,"temperature":0.2}`
	got, err := ollamaRequest([]byte(body), Config{Model: "fixture", ContextTokens: 32768, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Model    string
		Think    bool
		Stream   bool
		Messages []struct{ Role, Content string }
		Options  map[string]float64
	}
	if json.Unmarshal(got, &request) != nil || request.Model != "fixture" || request.Think || !request.Stream || len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Content != "firstsecond" || request.Options["num_ctx"] != 32768 || request.Options["num_predict"] != 1024 || request.Options["temperature"] != .2 {
		t.Fatal(string(got))
	}

	smaller, err := ollamaRequest([]byte(strings.Replace(body, `"max_tokens":1024`, `"max_tokens":512`, 1)), Config{Model: "fixture", ContextTokens: 32768, MaxOutputTokens: 1024})
	if err != nil || !strings.Contains(string(smaller), `"num_predict":512`) {
		t.Fatal("child output cap enlarged", string(smaller), err)
	}
	for _, field := range []string{`,"frequency_penalty":1`, `,"reasoning_effort":"high"`, `,"tools":[]`} {
		bad := strings.TrimSuffix(body, "}") + field + "}"
		if _, err := ollamaRequest([]byte(bad), Config{}); err == nil {
			t.Fatal("silently discarded unsupported control", field)
		}
	}
}

func TestOllamaCompletionRequiresVerifiedNativeTerminal(t *testing.T) {
	first := `{"model":"fixture","message":{"role":"assistant","content":"answer"},"done":false}` + "\n"
	end := `{"model":"fixture","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":4}` + "\n"
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"valid", first + end, true},
		{"unknown_counts", first + `{"model":"fixture","done":true,"done_reason":"stop"}`, true},
		{"truncated", first, false},
		{"wrong_model", strings.ReplaceAll(first+end, "fixture", "other"), false},
		{"length", first + strings.Replace(end, "stop", "length", 1), false},
		{"thinking", strings.Replace(first, `"content":"answer"`, `"content":"answer","thinking":"hidden"`, 1) + end, false},
		{"tools", strings.Replace(first, `"content":"answer"`, `"content":"answer","tool_calls":[{}]`, 1) + end, false},
		{"extra_terminal", first + end + end, false},
		{"error", first + `{"error":"private provider diagnostic"}`, false},
		{"partial_counts", first + strings.Replace(end, `,"eval_count":4`, "", 1), false},
		{"negative_counts", first + strings.Replace(end, `"eval_count":4`, `"eval_count":-1`, 1), false},
		{"oversized_frame", strings.Repeat("x", MaxRecordBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ollamaCompletion(strings.NewReader(tc.body), "fixture")
			if (err == nil) != tc.ok {
				t.Fatal(string(got), err)
			}
			if !tc.ok && len(got) != 0 {
				t.Fatal("malformed native response manufactured success")
			}
			if tc.ok && (!strings.Contains(string(got), `"finish_reason":"stop"`) || !strings.HasSuffix(string(got), "data: [DONE]\n\n")) {
				t.Fatal(string(got))
			}
			if tc.name == "unknown_counts" && strings.Contains(string(got), `"usage"`) {
				t.Fatal("unknown counts fabricated")
			}
		})
	}
}
