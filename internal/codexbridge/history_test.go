package codexbridge

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

func historyFixture() []providers.Message {
	return []providers.Message{
		{Role: "system", Content: "system context"},
		{Role: "user", Content: "previous question 世界"},
		{Role: "assistant", Content: "considering", ToolCalls: []providers.ToolCall{
			{ID: "call-α", Name: "old_tool", Arguments: json.RawMessage(`{"prompt":"a"}`)},
			{ID: "call-2", Name: "delegate", Arguments: json.RawMessage(`{}`)},
		}},
		{Role: "tool", ToolCallID: "call-2", Content: "second result"},
		{Role: "tool", ToolCallID: "call-α", Content: "first result"},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "continue"},
	}
}

func TestInitialHistoryTypedProjection(t *testing.T) {
	messages := historyFixture()
	before, _ := json.Marshal(messages)
	items, prompt, err := initialHistory(messages)
	if err != nil || prompt != "continue" || len(items) != 8 {
		t.Fatalf("projection: count=%d prompt=%q err=%v", len(items), prompt, err)
	}
	want := []string{
		`{"type":"message","role":"system","content":[{"type":"input_text","text":"system context"}]}`,
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"previous question 世界"}]}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"considering"}]}`,
		`{"type":"function_call","namespace":"darwin","name":"old_tool","call_id":"call-α","arguments":"{\"prompt\":\"a\"}"}`,
		`{"type":"function_call","namespace":"darwin","name":"delegate","call_id":"call-2","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"call-2","output":"second result"}`,
		`{"type":"function_call_output","call_id":"call-α","output":"first result"}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}`,
	}
	for i := range items {
		var gotValue, wantValue any
		if json.Unmarshal(items[i], &gotValue) != nil || json.Unmarshal([]byte(want[i]), &wantValue) != nil || !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("item %d shape mismatch", i)
		}
	}
	after, _ := json.Marshal(messages)
	if !bytes.Equal(before, after) {
		t.Fatal("projection mutated caller messages")
	}
	owned := bytes.Clone(items[3])
	messages[2].ToolCalls[0].Arguments[2] = 'X'
	if !bytes.Equal(owned, items[3]) {
		t.Fatal("projection retained caller argument storage")
	}
}

func TestInitialHistoryRejectsInvalid(t *testing.T) {
	cases := map[string]func([]providers.Message) []providers.Message{
		"empty":          func(m []providers.Message) []providers.Message { return nil },
		"last assistant": func(m []providers.Message) []providers.Message { m[6].Role = "assistant"; return m },
		"unknown role":   func(m []providers.Message) []providers.Message { m[0].Role = "developer"; return m },
		"invalid utf8":   func(m []providers.Message) []providers.Message { m[0].Content = string([]byte{255}); return m },
		"invalid args": func(m []providers.Message) []providers.Message {
			m[2].ToolCalls[0].Arguments = json.RawMessage(`[]`)
			return m
		},
		"invalid name": func(m []providers.Message) []providers.Message { m[2].ToolCalls[0].Name = "darwin.shell"; return m },
		"long id": func(m []providers.Message) []providers.Message {
			m[2].ToolCalls[0].ID = strings.Repeat("x", 257)
			m[4].ToolCallID = m[2].ToolCalls[0].ID
			return m
		},
		"control id": func(m []providers.Message) []providers.Message {
			m[2].ToolCalls[0].ID = "call\u0085"
			m[4].ToolCallID = m[2].ToolCalls[0].ID
			return m
		},
		"duplicate call":  func(m []providers.Message) []providers.Message { m[2].ToolCalls[1].ID = m[2].ToolCalls[0].ID; return m },
		"orphan result":   func(m []providers.Message) []providers.Message { m[3].ToolCallID = "missing"; return m },
		"incomplete pair": func(m []providers.Message) []providers.Message { return append(m[:4], m[5:]...) },
		"tools on user":   func(m []providers.Message) []providers.Message { m[6].ToolCalls = m[2].ToolCalls; return m },
		"oversized escaped history": func(m []providers.Message) []providers.Message {
			m[0].Content = strings.Repeat("<", codexrpc.DefaultMaxFrame/6)
			return m
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			messages := mutate(historyFixture())
			items, prompt, err := initialHistory(messages)
			if err == nil || items != nil || prompt != "" || err.Error() != failure(false).Error() || ValidateInitialMessages(messages) == nil {
				t.Fatal("invalid history accepted or unsafe partial error/result")
			}
		})
	}
}

func TestInitialHistoryFreshAndEmptyAssistant(t *testing.T) {
	items, prompt, err := initialHistory([]providers.Message{{Role: "user", Content: "fresh"}})
	if err != nil || len(items) != 0 || prompt != "fresh" {
		t.Fatal("fresh request changed")
	}
	messages := historyFixture()
	messages[2].Content = ""
	items, _, err = initialHistory(messages)
	if err != nil || len(items) != 7 {
		t.Fatal("empty assistant text must not hide its calls")
	}
	messages = []providers.Message{{Role: "user", Content: "old"}, {Role: "assistant"}, {Role: "user", Content: "next"}}
	items, _, err = initialHistory(messages)
	if err != nil || len(items) != 2 || !bytes.Contains(items[1], []byte(`"text":""`)) {
		t.Fatal("empty assistant without calls must retain its place in history")
	}
}

func TestInitialHistoryIncludesEnvelopeBudget(t *testing.T) {
	messages := []providers.Message{{Role: "user", Content: ""}, {Role: "user", Content: "next"}}
	items, _, err := initialHistory(messages)
	if err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(map[string]any{"threadId": strings.Repeat("<", 256), "items": items})
	frame, _ := json.Marshal(codexrpc.Envelope{ID: json.RawMessage("4"), Method: "thread/inject_items", Params: params})
	messages[0].Content = strings.Repeat("x", codexrpc.DefaultMaxFrame-len(frame))
	if err := ValidateInitialMessages(messages); err != nil {
		t.Fatal("exact bounded frame rejected")
	}
	messages[0].Content += "x"
	if err := ValidateInitialMessages(messages); err == nil {
		t.Fatal("oversized complete frame accepted")
	}
}
