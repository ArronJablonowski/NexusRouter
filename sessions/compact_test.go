package sessions

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func compactConversation() []providers.Message {
	return []providers.Message{
		{Role: "user", Content: "old request"},
		{Role: "assistant", Content: "checking", ToolCalls: []providers.ToolCall{
			{ID: "call-a", Name: "lookup", Arguments: json.RawMessage(`{"item":1}`)},
			{ID: "call-b", Name: "lookup", Arguments: json.RawMessage(`{"item":2}`)},
		}},
		{Role: "tool", ToolCallID: "call-a", Content: "first result"},
		{Role: "tool", ToolCallID: "call-b", Content: "second result"},
		{Role: "assistant", Content: "final answer"},
	}
}

func TestCompactRetainsCompleteToolPairsAtEveryBoundary(t *testing.T) {
	for _, tc := range []struct{ keep, removed, recent int }{{1, 4, 1}, {2, 1, 4}, {3, 1, 4}, {4, 1, 4}, {5, 0, 5}, {100, 0, 5}} {
		out, err := Compact(compactConversation(), tc.keep, Summary{Decisions: []string{"Retain decision"}})
		if err != nil || out.Version != 1 || out.RemovedMessages != tc.removed || len(out.Recent) != tc.recent {
			t.Fatalf("keep %d: %+v, %v", tc.keep, out, err)
		}
		if err := providers.ValidateMessages(out.Recent); err != nil {
			t.Fatal("invalid retained conversation", err)
		}
	}
}

func TestCompactValidatesRemovedHistoryAndToolFields(t *testing.T) {
	for name, mutate := range map[string]func([]providers.Message) []providers.Message{
		"unknown-role":      func(m []providers.Message) []providers.Message { m[0].Role = "alien"; return m },
		"non-tool-call-id":  func(m []providers.Message) []providers.Message { m[0].ToolCallID = "call-a"; return m },
		"call-missing-name": func(m []providers.Message) []providers.Message { m[1].ToolCalls[0].Name = ""; return m },
		"call-missing-id":   func(m []providers.Message) []providers.Message { m[1].ToolCalls[0].ID = ""; return m },
		"invalid-arguments": func(m []providers.Message) []providers.Message {
			m[1].ToolCalls[0].Arguments = json.RawMessage(`{`)
			return m
		},
		"array-arguments": func(m []providers.Message) []providers.Message {
			m[1].ToolCalls[0].Arguments = json.RawMessage(`[]`)
			return m
		},
		"orphan-result": func(m []providers.Message) []providers.Message { m[2].ToolCallID = "unknown"; return m },
		"result-has-calls": func(m []providers.Message) []providers.Message {
			m[2].ToolCalls = []providers.ToolCall{{ID: "nested", Name: "lookup", Arguments: json.RawMessage(`{}`)}}
			return m
		},
		"duplicate-result":             func(m []providers.Message) []providers.Message { m[3].ToolCallID = "call-a"; return m },
		"duplicate-call":               func(m []providers.Message) []providers.Message { m[1].ToolCalls[1].ID = "call-a"; return m },
		"incomplete-batch":             func(m []providers.Message) []providers.Message { return append(m[:3], m[4]) },
		"pending-at-end":               func(m []providers.Message) []providers.Message { return m[:3] },
		"invalid-utf8-removed-content": func(m []providers.Message) []providers.Message { m[0].Content = string([]byte{0xff}); return m },
		"invalid-utf8-recent-content":  func(m []providers.Message) []providers.Message { m[4].Content = string([]byte{0xff}); return m },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compact(mutate(compactConversation()), 1, Summary{}); !errors.Is(err, ErrHistory) {
				t.Fatalf("want ErrHistory, got %v", err)
			}
		})
	}
	for _, keep := range []int{0, -1} {
		if _, err := Compact(compactConversation(), keep, Summary{}); !errors.Is(err, ErrHistory) {
			t.Fatal(keep, err)
		}
	}
	if _, err := Compact(nil, 1, Summary{}); !errors.Is(err, ErrHistory) {
		t.Fatal(err)
	}
}

func compactSummaryFields(s *Summary) []*[]string {
	return []*[]string{&s.Decisions, &s.PendingWork, &s.Failures, &s.Artifacts}
}

func TestCompactRejectsInvalidSummaryEntriesAndCounts(t *testing.T) {
	for category := 0; category < 4; category++ {
		for _, value := range []string{"", " \t\n", "\u2003", string([]byte{0xff})} {
			summary := Summary{}
			*compactSummaryFields(&summary)[category] = []string{value}
			if _, err := Compact(compactConversation(), 1, summary); !errors.Is(err, ErrHistory) {
				t.Fatalf("category %d value %q: %v", category, value, err)
			}
		}
		for _, count := range []int{128, 129} {
			summary := Summary{}
			entries := make([]string, count)
			for i := range entries {
				entries[i] = "item"
			}
			*compactSummaryFields(&summary)[category] = entries
			_, err := Compact(compactConversation(), 1, summary)
			if count == 128 && err != nil {
				t.Fatal("valid category limit rejected", category, err)
			}
			if count == 129 && !errors.Is(err, ErrHistory) {
				t.Fatal("category limit not enforced", category, err)
			}
		}
	}
}

func TestCompactSummarySerializedByteLimit(t *testing.T) {
	base := Summary{Decisions: []string{"x"}}
	b, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []int{0, 1} {
		summary := Summary{Decisions: []string{strings.Repeat("x", (64<<10)-len(b)+1+extra)}}
		encoded, _ := json.Marshal(summary)
		if len(encoded) != (64<<10)+extra {
			t.Fatal("fixture budget", len(encoded))
		}
		_, err := Compact(compactConversation(), 1, summary)
		if extra == 0 && err != nil {
			t.Fatal("exact byte limit rejected", err)
		}
		if extra == 1 && !errors.Is(err, ErrHistory) {
			t.Fatal("byte limit not enforced", err)
		}
	}
	// Escaped JSON bytes count, rather than just input string length.
	summary := Summary{Decisions: []string{"x" + strings.Repeat("\n", 33000)}}
	if _, err := Compact(compactConversation(), 1, summary); !errors.Is(err, ErrHistory) {
		t.Fatal("escaped byte limit not enforced", err)
	}
}

func TestCompactOwnsSummaryAndNestedRecentData(t *testing.T) {
	for _, mutateInput := range []bool{true, false} {
		messages := compactConversation()
		summary := Summary{Decisions: []string{"decision"}, PendingWork: []string{"pending"}, Failures: []string{"failure"}, Artifacts: []string{"artifact"}}
		out, err := Compact(messages, 4, summary)
		if err != nil {
			t.Fatal(err)
		}
		if mutateInput {
			for _, field := range compactSummaryFields(&summary) {
				(*field)[0] = "changed"
			}
			messages[1].ToolCalls[0].Arguments[8] = '9'
			messages[1].ToolCalls[1].Name = "changed"
			messages[1].Content = "changed"
			if out.Recent[0].Content != "checking" || out.Recent[0].ToolCalls[1].Name != "lookup" || string(out.Recent[0].ToolCalls[0].Arguments) != `{"item":1}` {
				t.Fatal("recent aliases input", out.Recent)
			}
			for _, field := range compactSummaryFields(&out.Summary) {
				if (*field)[0] == "changed" {
					t.Fatal("summary aliases input")
				}
			}
		} else {
			for _, field := range compactSummaryFields(&out.Summary) {
				(*field)[0] = "changed"
			}
			out.Recent[0].ToolCalls[0].Arguments[8] = '9'
			out.Recent[0].ToolCalls[1].Name = "changed"
			out.Recent[0].Content = "changed"
			if messages[1].Content != "checking" || messages[1].ToolCalls[1].Name != "lookup" || string(messages[1].ToolCalls[0].Arguments) != `{"item":1}` {
				t.Fatal("input aliases recent", messages)
			}
			for _, field := range compactSummaryFields(&summary) {
				if (*field)[0] == "changed" {
					t.Fatal("input aliases summary")
				}
			}
		}
	}
}
