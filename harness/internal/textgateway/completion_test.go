package textgateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func completionFixture(model string) string {
	return `data: {"id":"one","object":"chat.completion.chunk","model":"` + model + `","choices":[{"index":0,"delta":{"role":"assistant","content":"answer"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"one","object":"chat.completion.chunk","model":"` + model + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
}

func TestCompletionRequiresUnambiguousNormalTerminal(t *testing.T) {
	good := completionFixture("model")
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", good, true},
		{"CRLF", strings.ReplaceAll(good, "\n", "\r\n"), true},
		{"comments", ": keepalive\n\n" + good, true},
		{"multiline", strings.Replace(good, `"object":`, "\ndata: \"object\":", 1), true},
		{"length", strings.Replace(good, `"stop"`, `"length"`, 1), false},
		{"missing-done", strings.Replace(good, "data: [DONE]\n\n", "", 1), false},
		{"only-done", "data: [DONE]\n\n", false},
		{"truncated-frame", strings.TrimSuffix(good, "\n\n"), false},
		{"wrong-model", strings.Replace(good, `"model":"model"`, `"model":"fallback"`, 1), false},
		{"changed-id", strings.Replace(good, `"id":"one"`, `"id":"other"`, 1), false},
		{"tools", strings.Replace(good, `"role":"assistant"`, `"tool_calls":[{}]`, 1), false},
		{"refusal", strings.Replace(good, `"role":"assistant"`, `"refusal":"no"`, 1), false},
		{"reasoning", strings.Replace(good, `"role":"assistant"`, `"reasoning":"hidden"`, 1), false},
		{"wrong-choice", strings.Replace(good, `"index":0`, `"index":1`, 1), false},
		{"duplicate", strings.Replace(good, `"model":"model"`, `"model":"fallback","model":"model"`, 1), false},
		{"after-done", good + good, false},
		{"error", strings.Replace(good, `"id":"one"`, `"error":{},"id":"one"`, 1), false},
		{"oversized", strings.Repeat("x", MaxRecordBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := verifyCompletion(strings.NewReader(tc.body), "model")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if !tc.valid && (got.Text != "" || len(got.Stream) != 0 || got.Usage != nil) {
				t.Fatal("unverified output released")
			}
			if tc.valid && got.Text != "answer" {
				t.Fatal("text mismatch")
			}
		})
	}
}

func TestCompletionUsage(t *testing.T) {
	good := completionFixture("model")
	for _, usage := range []string{
		`{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}`,
		`{"prompt_tokens":20,"completion_tokens":3,"total_tokens":22}`,
		`{"prompt_tokens":-1,"completion_tokens":3,"total_tokens":2}`,
		`{"prompt_tokens":20}`,
	} {
		chunk := `data: {"id":"one","object":"chat.completion.chunk","model":"model","choices":[],"usage":` + usage + "}\n\n"
		stream := strings.Replace(good, "data: [DONE]", chunk+"data: [DONE]", 1)
		_, err := verifyCompletion(strings.NewReader(stream), "model")
		if (err == nil) != strings.Contains(usage, `"total_tokens":23`) {
			t.Fatal("invalid usage acceptance", usage, err)
		}
	}
}

func TestGatewayDoesNotReleaseTruncatedCompletion(t *testing.T) {
	for _, good := range []bool{false, true} {
		c := gatewayFixture()
		c.Transport = policyTransport(func(*http.Request) (*http.Response, error) {
			stream := completionFixture("model")
			if !good {
				stream = strings.Replace(stream, `"stop"`, `"length"`, 1)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
		})
		base, key, verified, closeGateway, err := Start(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verified(); err == nil {
			t.Fatal("verified before dispatch")
		}
		code := gatewayCall(t, base, key, gatewayBody)
		result, err := verified()
		closeGateway()
		if (err == nil) != good || (code == 200) != good {
			t.Fatal("invalid gateway acceptance", code, err)
		}
		if good && result.Text != "answer" {
			t.Fatal("verified text mismatch")
		}
	}
}

func TestMeasuredUsagePresenceAndTerminalBinding(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		want        bool
		in, out     int64
	}{
		{"absent", "", false, 0, 0},
		{"zero", `,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, true, 0, 0},
		{"measured", `,"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}`, true, 20, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := strings.Replace(completionFixture("model"), `"finish_reason":"stop"}]}`, `"finish_reason":"stop"}]`+tc.usage+`}`, 1)
			got, err := verifyCompletion(strings.NewReader(stream), "model")
			if err != nil || (got.Usage != nil) != tc.want {
				t.Fatal(got, err)
			}
			if got.Usage != nil && (got.Usage.InputTokens != tc.in || got.Usage.OutputTokens != tc.out) {
				t.Fatal("wrong measured counts")
			}
			broken := strings.Replace(stream, "data: [DONE]\n\n", "", 1)
			got, err = verifyCompletion(strings.NewReader(broken), "model")
			if err == nil || got.Usage != nil {
				t.Fatal("incomplete stream released usage as completed")
			}
		})
	}
}

func TestNativeOllamaMeasuredUsage(t *testing.T) {
	for _, counts := range []string{"", `,"prompt_eval_count":17,"eval_count":4`} {
		native := `{"model":"model","message":{"role":"assistant","content":"answer"},"done":true,"done_reason":"stop"` + counts + "}\n"
		stream, err := ollamaCompletion(strings.NewReader(native), "model")
		if err != nil {
			t.Fatal(err)
		}
		got, err := verifyCompletion(strings.NewReader(string(stream)), "model")
		if err != nil {
			t.Fatal(err)
		}
		if counts == "" {
			if got.Usage != nil {
				t.Fatal("invented counts")
			}
		} else if got.Usage == nil || got.Usage.InputTokens != 17 || got.Usage.OutputTokens != 4 {
			t.Fatal("lost native counts")
		}
	}
}
