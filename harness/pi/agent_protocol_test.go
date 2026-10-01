package pi

import (
	"strings"
	"testing"
)

func agentTranscript() []string {
	base := transcript()
	proposal := strings.Replace(base[4], `"stopReason":"stop"`, `"stopReason":"toolUse"`, 1)
	proposal = strings.Replace(proposal, `{"type":"text","text":"answer"}`, `{"type":"toolCall","id":"one","name":"lookup","arguments":{"path":"fixture"}}`, 1)
	return []string{base[0], base[1], base[2], base[3], proposal,
		`{"type":"tool_execution_start","toolCallId":"one","toolName":"lookup"}`,
		`{"type":"tool_execution_end","toolCallId":"one","toolName":"lookup","isError":false}`,
		`{"type":"message_end","message":{"role":"toolResult","toolCallId":"one","toolName":"lookup","isError":false,"content":[{"type":"text","text":"result"}]}}`,
		base[5], base[3], base[4], base[5], base[6], base[7],
	}
}
func TestAgentRPCCompletionAndRejectedLifecycles(t *testing.T) {
	good := agentTranscript()
	p, _ := NewAgentProtocol("fixture", "model", 3, []string{"lookup"})
	for i, line := range good {
		settled, e := p.Consume([]byte(line))
		if e != nil {
			t.Fatal(i, e)
		}
		_, e = p.Result()
		if i < len(good)-1 && (settled || e == nil) {
			t.Fatal("premature final")
		}
	}
	if r, e := p.Result(); e != nil || r.Text != "answer" {
		t.Fatal(r, e)
	}
	if _, e := p.Consume([]byte(good[len(good)-1])); e == nil {
		t.Fatal("duplicate settlement")
	}
	cases := map[string]func([]string) []string{
		"unknown-tool": func(s []string) []string { s[4] = strings.Replace(s[4], "lookup", "shell", 1); return s },
		"nested-tool": func(s []string) []string {
			s[5] = strings.Replace(s[5], `"toolName"`, `"parentToolCallId":"parent","toolName"`, 1)
			return s
		},
		"missing-tool-start":  func(s []string) []string { return append(s[:5], s[6:]...) },
		"wrong-tool-id":       func(s []string) []string { s[6] = strings.Replace(s[6], "one", "other", 1); return s },
		"wrong-tool-name":     func(s []string) []string { s[6] = strings.Replace(s[6], "lookup", "other", 1); return s },
		"missing-tool-result": func(s []string) []string { return append(s[:7], s[8:]...) },
		"failure-mismatch":    func(s []string) []string { s[7] = strings.Replace(s[7], "false", "true", 1); return s },
		"duplicate-result":    func(s []string) []string { s[8] = s[7]; return s },
		"unfinished-turn":     func(s []string) []string { s[8] = s[9]; return s },
		"wrong-provider": func(s []string) []string {
			s[10] = strings.Replace(s[10], `"provider":"fixture"`, `"provider":"fallback"`, 1)
			return s
		},
		"wrong-response-model": func(s []string) []string {
			s[10] = strings.Replace(s[10], `"stopReason"`, `"responseModel":"other","stopReason"`, 1)
			return s
		},
		"retry":        func(s []string) []string { s[12] = strings.Replace(s[12], "false", "true", 1); return s },
		"compaction":   func(s []string) []string { s[9] = `{"type":"auto_compaction_start"}`; return s },
		"early-settle": func(s []string) []string { s[8] = s[13]; return s },
		"duplicate-key": func(s []string) []string {
			s[4] = strings.Replace(s[4], `"model":"model"`, `"model":"other","model":"model"`, 1)
			return s
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := NewAgentProtocol("fixture", "model", 3, []string{"lookup"})
			rejected := false
			for _, line := range mutate(agentTranscript()) {
				if _, e := p.Consume([]byte(line)); e != nil {
					rejected = true
					break
				}
			}
			if !rejected {
				t.Fatal("invalid lifecycle accepted")
			}
		})
	}
	p, _ = NewAgentProtocol("fixture", "model", 1, []string{"lookup"})
	for i, line := range good {
		if _, e := p.Consume([]byte(line)); e != nil {
			if i != 9 {
				t.Fatal("wrong turn-limit boundary", i, e)
			}
			return
		}
	}
	t.Fatal("turn limit exceeded")
}
