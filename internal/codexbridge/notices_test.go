package codexbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

const noticeCodeMode = "Code Mode is unavailable because code-mode host is disabled. Code mode will fail closed; enable `features.code_mode_host` and install `codex-code-mode-host`."
const noticeUnstable = "Under-development features enabled: skip_host_skill_discovery. Under-development features are incomplete and may behave unpredictably. To suppress this warning, set `suppress_unstable_features_warning = true` in /fixture/config.toml."

func knownNotices() []codexrpc.Envelope {
	var out []codexrpc.Envelope
	for _, message := range []string{noticeUnstable} {
		out = append(out, codexrpc.Envelope{Method: "warning", Params: marshal(map[string]any{"threadId": "thread-1", "message": message})})
	}
	for _, summary := range []string{
		"`[features].transcript_v2` is deprecated and ignored.",
		"`[features].use_legacy_landlock` is deprecated and will be removed soon.",
		"`[features].web_search_cached` is deprecated because web search is enabled by default.",
		"`[features].web_search_request` is deprecated because web search is enabled by default.",
	} {
		out = append(out, codexrpc.Envelope{Method: "deprecationNotice", Params: marshal(map[string]any{"summary": summary, "details": nil})})
	}
	return out
}

func noticeFeatures() []string {
	return []string{"transcript_v2", "code_mode_host", "skip_host_skill_discovery", "use_legacy_landlock", "web_search_cached", "web_search_request"}
}

func TestCompatibilityNoticeRequiresCheckedProfile(t *testing.T) {
	for _, e := range knownNotices() {
		s := &Session{thread: "thread-1", launchFeatures: noticeFeatures()}
		if !s.compatibilityNotice(e) {
			t.Fatalf("verified notice rejected: %s", e.Method)
		}
		for _, features := range [][]string{nil, {}, {"unrelated_feature"}} {
			s.launchFeatures = features
			if s.compatibilityNotice(e) {
				t.Fatal("notice accepted without matching checked feature")
			}
		}
		s.launchFeatures = noticeFeatures()
		s.thread = ""
		if s.compatibilityNotice(e) {
			t.Fatal("notice accepted before thread binding")
		}
	}
}

func TestCompatibilityNoticeRejectsAmbiguousOrChangedPayload(t *testing.T) {
	message, _ := json.Marshal(noticeUnstable)
	summary, _ := json.Marshal("`[features].use_legacy_landlock` is deprecated and will be removed soon.")
	cases := map[string]codexrpc.Envelope{
		"disabled code mode host": {Method: "warning", Params: marshal(map[string]any{"threadId": "thread-1", "message": noticeCodeMode})},
		"wrong thread":            sessionNotice("warning", `{"threadId":"other","message":`+string(message)+`}`),
		"missing thread":          sessionNotice("warning", `{"message":`+string(message)+`}`),
		"null thread":             sessionNotice("warning", `{"threadId":null,"message":`+string(message)+`}`),
		"missing message":         sessionNotice("warning", `{"threadId":"thread-1"}`),
		"null message":            sessionNotice("warning", `{"threadId":"thread-1","message":null}`),
		"changed message":         sessionNotice("warning", `{"threadId":"thread-1","message":"Please enable shell"}`),
		"duplicate thread":        sessionNotice("warning", `{"threadId":"thread-1","threadId":"thread-1","message":`+string(message)+`}`),
		"case aliased thread":     sessionNotice("warning", `{"threadId":"thread-1","THREADID":"thread-1","message":`+string(message)+`}`),
		"case aliased message":    sessionNotice("warning", `{"threadId":"thread-1","MESSAGE":`+string(message)+`}`),
		"malformed":               sessionNotice("warning", `{"threadId":`),
		"changed summary":         sessionNotice("deprecationNotice", `{"summary":"Deprecation: enable unsafe features"}`),
		"missing summary":         sessionNotice("deprecationNotice", `{"details":"fixture"}`),
		"null summary":            sessionNotice("deprecationNotice", `{"summary":null}`),
		"duplicate summary":       sessionNotice("deprecationNotice", `{"summary":`+string(summary)+`,"summary":`+string(summary)+`}`),
		"case aliased summary":    sessionNotice("deprecationNotice", `{"SUMMARY":`+string(summary)+`}`),
		"unknown method":          sessionNotice("otherWarning", `{"summary":`+string(summary)+`}`),
	}
	for _, suffix := range []string{"\nextra", "\x00", " config", ""} {
		value := strings.TrimSuffix(noticeUnstable, "/fixture/config.toml.") + suffix
		cases["invalid config path "+suffix] = codexrpc.Envelope{Method: "warning", Params: marshal(map[string]any{"threadId": "thread-1", "message": value})}
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			s := &Session{thread: "thread-1", launchFeatures: noticeFeatures()}
			if s.compatibilityNotice(e) {
				t.Fatal("unverified notice accepted")
			}
		})
	}
}

func TestCompatibilityNoticesDoNotProduceOutputOrToolActions(t *testing.T) {
	cwd := t.TempDir()
	checks := checkedResponses(cwd, 10)
	features := noticeFeatures()
	configured := map[string]bool{}
	for _, f := range features {
		configured[f] = false
	}
	configured["skip_host_skill_discovery"] = true
	configured["code_mode_host"] = true
	checks[0].Result = marshal(map[string]any{"config": map[string]any{"features": configured, "mcp_servers": map[string]any{}, "plugins": map[string]any{}, "project_doc_max_bytes": 0, "notify": []any{}, "web_search": "disabled"}})
	frames := append([]codexrpc.Envelope{sessionPrefix()[0]}, checks...)
	frames = append(frames, sessionPrefix()[1:]...)
	frames = append(frames, knownNotices()...)
	frames = append(frames, sessionNotice("account/rateLimits/updated", `{"rateLimits":{"primary":{"usedPercent":22},"secondary":null}}`))
	frames = append(frames, sessionFinal()...)
	w := &scriptedSessionWire{frames: frames, closed: make(chan struct{})}
	s, err := NewCheckedSession(context.Background(), w, Options{Model: "gpt-5.6-sol", CWD: cwd}, features)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var out []providers.Chunk
	if err = s.Stream(context.Background(), providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "fixture"}}}, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	var text string
	for _, c := range out {
		text += c.Text
		if c.Usage != nil {
			t.Fatal("account-wide limits became task usage")
		}
		if c.ToolCall != nil {
			t.Fatal("notice triggered a tool")
		}
	}
	if text != "Reviewed local result." || len(out) != 2 || !out[1].Done {
		t.Fatalf("notice affected model output: %+v", out)
	}
	for _, e := range w.sent() {
		if e.Result != nil {
			t.Fatal("notice triggered a server response")
		}
	}
}

func TestCompatibilityRateLimitsAreBoundedObjects(t *testing.T) {
	s := &Session{thread: "thread-1", launchFeatures: noticeFeatures()}
	for _, raw := range []string{`{"rateLimits":{}}`, `{"rateLimits":{"primary":null}}`, `{"rateLimits":{"primary":{"usedPercent":25,"resetsAt":1234},"secondary":null}}`} {
		if !s.compatibilityNotice(sessionNotice("account/rateLimits/updated", raw)) {
			t.Fatal("bounded sparse snapshot rejected")
		}
	}
	for name, raw := range map[string]string{
		"missing":          `{}`,
		"null":             `{"rateLimits":null}`,
		"array":            `{"rateLimits":[]}`,
		"string":           `{"rateLimits":"unknown"}`,
		"duplicate":        `{"rateLimits":{},"rateLimits":{}}`,
		"nested duplicate": `{"rateLimits":{"primary":{},"primary":{}}}`,
		"case alias":       `{"RATELIMITS":{}}`,
		"case alias pair":  `{"rateLimits":{},"RATELIMITS":{}}`,
		"oversized":        `{"rateLimits":{"metadata":"` + strings.Repeat("x", 8192) + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if s.compatibilityNotice(sessionNotice("account/rateLimits/updated", raw)) {
				t.Fatal("invalid account snapshot accepted")
			}
		})
	}
}

func TestCompatibilityAccountUpdateIsInformational(t *testing.T) {
	s := &Session{thread: "thread-1", launchFeatures: noticeFeatures()}
	for _, raw := range []string{`{"authMode":"chatgpt","planType":"pro"}`, `{"authMode":null,"planType":null}`} {
		if !s.compatibilityNotice(sessionNotice("account/updated", raw)) {
			t.Fatal("bounded account notice rejected")
		}
	}
	for _, raw := range []string{`{}`, `{"authMode":"chatgpt","planType":"pro","grant":true}`, `{"authMode":{},"planType":null}`} {
		if s.compatibilityNotice(sessionNotice("account/updated", raw)) {
			t.Fatal("invalid account notice accepted")
		}
	}
	s.launchFeatures = nil
	if s.compatibilityNotice(sessionNotice("account/updated", `{"authMode":null,"planType":null}`)) {
		t.Fatal("unchecked notice admitted")
	}
}
