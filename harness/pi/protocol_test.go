package pi

import (
	"strings"
	"testing"
)

func transcript() []string {
	return []string{
		`{"type":"response","id":"state","command":"get_state","success":true,"data":{"model":{"provider":"fixture","id":"model","baseUrl":"http://127.0.0.1/v1","contextWindow":16384,"maxTokens":1024}}}`,
		`{"type":"response","id":"prompt","command":"prompt","success":true,"data":{"disposition":"started"}}`,
		`{"type":"agent_start"}`,
		`{"type":"turn_start"}`,
		`{"type":"message_end","message":{"usage":{"input":20,"output":4,"cacheRead":0,"cacheWrite":0,"totalTokens":24,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"role":"assistant","provider":"fixture","model":"model","stopReason":"stop","content":[{"type":"text","text":"answer"}]}}`,
		`{"type":"turn_end"}`,
		`{"type":"agent_end","willRetry":false}`,
		`{"type":"agent_settled"}`,
	}
}
func TestCompletionBoundary(t *testing.T) {
	p, _ := NewProtocol("fixture", "model")
	for i, line := range transcript() {
		settled, err := p.Consume([]byte(line))
		if err != nil {
			t.Fatal(i, err)
		}
		_, err = p.Result()
		if i < 7 && (settled || err == nil) {
			t.Fatal("premature completion", i)
		}
		if i == 7 && (!settled || err != nil) {
			t.Fatal("missing settled result", err)
		}
	}
	if _, err := p.Consume([]byte(`{"type":"agent_settled"}`)); err == nil {
		t.Fatal("duplicate settlement accepted")
	}
}
func TestRejectedTranscripts(t *testing.T) {
	tests := map[string]func([]string) []string{
		"wrong provider": func(s []string) []string { s[4] = strings.Replace(s[4], "fixture", "other", 1); return s },
		"wrong actual model": func(s []string) []string {
			s[4] = strings.Replace(s[4], `"stopReason"`, `"responseModel":"other","stopReason"`, 1)
			return s
		},
		"truncated output": func(s []string) []string { s[4] = strings.Replace(s[4], `"stop"`, `"length"`, 1); return s },
		"tool output": func(s []string) []string {
			s[4] = strings.Replace(s[4], `"type":"text"`, `"type":"toolCall"`, 1)
			return s
		},
		"retry":               func(s []string) []string { s[6] = `{"type":"agent_end","willRetry":true}`; return s },
		"early settlement":    func(s []string) []string { s[5] = s[7]; return s },
		"missing turn end":    func(s []string) []string { return append(s[:5], s[6:]...) },
		"duplicate turn end":  func(s []string) []string { s[6] = s[5]; return s },
		"queued prompt":       func(s []string) []string { s[1] = strings.Replace(s[1], "started", "queued", 1); return s },
		"wrong context":       func(s []string) []string { s[0] = strings.Replace(s[0], "16384", "8192", 1); return s },
		"wrong endpoint":      func(s []string) []string { s[0] = strings.Replace(s[0], "127.0.0.1", "127.0.0.2", 1); return s },
		"wrong output budget": func(s []string) []string { s[0] = strings.Replace(s[0], "1024", "2048", 1); return s },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p, _ := NewProtocol("fixture", "model")
			p.expectURL = "http://127.0.0.1/v1"
			p.expectContext = 16384
			p.expectOutput = 1024
			for _, line := range mutate(transcript()) {
				if _, err := p.Consume([]byte(line)); err != nil {
					return
				}
			}
			t.Fatal("invalid transcript accepted")
		})
	}
}
