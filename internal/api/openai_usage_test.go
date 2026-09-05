package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func chatUsageRequest(options string) string {
	return strings.TrimSuffix(chatFixture, "}") + `,"stream":true,"stream_options":` + options + `}`
}

func usageFrames(t *testing.T, body string) ([]map[string]json.RawMessage, bool) {
	t.Helper()
	var frames []map[string]json.RawMessage
	done := false
	for _, raw := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if done {
			t.Fatal("frame after DONE")
		}
		if !strings.HasPrefix(raw, "data: ") {
			t.Fatal("invalid SSE framing", raw)
		}
		if raw == "data: [DONE]" {
			done = true
			continue
		}
		var frame map[string]json.RawMessage
		if json.Unmarshal([]byte(strings.TrimPrefix(raw, "data: ")), &frame) != nil {
			t.Fatal("invalid JSON chunk", raw)
		}
		frames = append(frames, frame)
	}
	return frames, done
}

func TestChatUsageStreamingExactAndZeroCounts(t *testing.T) {
	for _, usage := range []providers.Usage{{InputTokens: 17, OutputTokens: 9}, {InputTokens: 0, OutputTokens: 0}, {InputTokens: math.MaxInt64, OutputTokens: 0}} {
		t.Run(usageCaseName(usage), func(t *testing.T) {
			s := services()
			calls := 0
			s.RunTextStream = func(_ context.Context, _ app.Request, emit func(string) error) (app.Result, error) {
				calls++
				for _, text := range []string{"Hello", " 🌍"} {
					if err := emit(text); err != nil {
						return app.Result{}, err
					}
				}
				return app.Result{Text: "Hello 🌍", FinishReason: "stop", Usage: &usage}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/chat/completions", chatUsageRequest(`{"include_usage":true}`)))
			if w.Code != 200 || calls != 1 || w.Header().Get("Content-Type") != "text/event-stream" {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
			frames, done := usageFrames(t, w.Body.String())
			if !done || len(frames) != 5 {
				t.Fatal(w.Body.String())
			}
			for i, f := range frames {
				for _, key := range []string{"id", "object", "created", "model"} {
					if len(f[key]) == 0 || string(f[key]) != string(frames[0][key]) {
						t.Fatal("inconsistent metadata", key, i)
					}
				}
				if i < len(frames)-1 && string(f["usage"]) != "null" {
					t.Fatal("regular chunk missing null usage", i, string(f["usage"]))
				}
			}
			last := frames[len(frames)-1]
			if string(last["choices"]) != "[]" {
				t.Fatal("usage chunk has choices", string(last["choices"]))
			}
			var totals map[string]int64
			if json.Unmarshal(last["usage"], &totals) != nil || len(totals) != 3 || totals["prompt_tokens"] != usage.InputTokens || totals["completion_tokens"] != usage.OutputTokens || totals["total_tokens"] != usage.InputTokens+usage.OutputTokens {
				t.Fatal(string(last["usage"]))
			}
			var finish []struct {
				FinishReason string `json:"finish_reason"`
			}
			if json.Unmarshal(frames[len(frames)-2]["choices"], &finish) != nil || len(finish) != 1 || finish[0].FinishReason != "stop" {
				t.Fatal("finish did not precede usage", w.Body.String())
			}
		})
	}
}

func usageCaseName(u providers.Usage) string { b, _ := json.Marshal(u); return string(b) }

func TestChatUsageStreamingOptOutKeepsExistingChunks(t *testing.T) {
	for _, options := range []string{"null", `{}`, `{"include_usage":false}`} {
		t.Run(options, func(t *testing.T) {
			s := services()
			s.RunTextStream = func(_ context.Context, _ app.Request, emit func(string) error) (app.Result, error) {
				if err := emit("answer"); err != nil {
					return app.Result{}, err
				}
				return app.Result{Text: "answer", FinishReason: "stop", Usage: &providers.Usage{InputTokens: 3, OutputTokens: 4}}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/chat/completions", chatUsageRequest(options)))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			frames, done := usageFrames(t, w.Body.String())
			if !done || len(frames) != 3 {
				t.Fatal(w.Body.String())
			}
			for _, f := range frames {
				if _, ok := f["usage"]; ok {
					t.Fatal("opt-out added usage", w.Body.String())
				}
				if string(f["choices"]) == "[]" {
					t.Fatal("opt-out added totals chunk")
				}
			}
		})
	}
}

func TestChatUsageStreamingMissingInvalidOrOverflowNeverFinishes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage *providers.Usage
	}{{"missing", nil}, {"negative_input", &providers.Usage{InputTokens: -1, OutputTokens: 2}}, {"negative_output", &providers.Usage{InputTokens: 1, OutputTokens: -2}}, {"overflow", &providers.Usage{InputTokens: math.MaxInt64, OutputTokens: 1}}} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			s.RunTextStream = func(_ context.Context, _ app.Request, emit func(string) error) (app.Result, error) {
				if err := emit("provisional"); err != nil {
					return app.Result{}, err
				}
				return app.Result{Text: "provisional", FinishReason: "stop", Usage: tc.usage}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/chat/completions", chatUsageRequest(`{"include_usage":true}`)))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			frames, done := usageFrames(t, w.Body.String())
			if done || len(frames) != 3 {
				t.Fatal(w.Body.String())
			}
			if !strings.Contains(string(frames[2]["error"]), `"code":"usage_unavailable"`) {
				t.Fatal(w.Body.String())
			}
			for _, f := range frames {
				if string(f["choices"]) == "[]" || strings.Contains(string(f["choices"]), `"finish_reason":"`) {
					t.Fatal("invalid usage produced completion", w.Body.String())
				}
				if u, ok := f["usage"]; ok && string(u) != "null" {
					t.Fatal("invented usage", string(u))
				}
			}
		})
	}
}

func TestChatUsageStreamingFailedExecutionNeverReportsTotals(t *testing.T) {
	s := services()
	s.RunTextStream = func(_ context.Context, _ app.Request, emit func(string) error) (app.Result, error) {
		if err := emit("partial"); err != nil {
			return app.Result{}, err
		}
		return app.Result{Text: "partial", FinishReason: "stop", Usage: &providers.Usage{InputTokens: 5, OutputTokens: 7}}, errors.New("private backend failure")
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/chat/completions", chatUsageRequest(`{"include_usage":true}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	frames, done := usageFrames(t, w.Body.String())
	if done || len(frames) != 3 || strings.Contains(w.Body.String(), "private backend") {
		t.Fatal(w.Body.String())
	}
	for _, f := range frames {
		if string(f["choices"]) == "[]" || strings.Contains(string(f["choices"]), `"finish_reason":"`) {
			t.Fatal("failure reported completion")
		}
		if u, ok := f["usage"]; ok && string(u) != "null" {
			t.Fatal("failure reported totals")
		}
	}
}

func TestChatUsageStreamingRejectsUnsupportedOptions(t *testing.T) {
	var invalid []string
	for _, options := range []string{`{"unknown":true}`, `{"include_usage":true,"include_usage":false}`, `{"Include_Usage":true}`, `{"include_usage":null}`, `{"include_usage":"true"}`, `{"include_usage":1}`, `{"include_obfuscation":true}`, `[]`, `true`, `"value"`} {
		invalid = append(invalid, chatUsageRequest(options))
	}
	for _, options := range []string{`null`, `{}`, `{"include_usage":false}`, `{"include_usage":true}`} {
		invalid = append(invalid, strings.TrimSuffix(chatFixture, "}")+`,"stream_options":`+options+`}`, strings.TrimSuffix(chatFixture, "}")+`,"stream":false,"stream_options":`+options+`}`)
	}
	invalid = append(invalid, strings.TrimSuffix(chatUsageRequest(`{}`), "}")+`,"stream_options":{}}`, strings.Replace(chatUsageRequest(`{}`), `"stream_options"`, `"Stream_Options"`, 1))
	for _, body := range invalid {
		t.Run(body, func(t *testing.T) {
			s := services()
			s.RunTextStream = func(context.Context, app.Request, func(string) error) (app.Result, error) {
				t.Error("invalid usage request dispatched stream")
				return app.Result{}, nil
			}
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Error("invalid usage request dispatched completion")
				return app.Result{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/chat/completions", body))
			if w.Code != 400 || !strings.Contains(w.Body.String(), `"type":"invalid_request_error"`) {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
