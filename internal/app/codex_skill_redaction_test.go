package app

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func workflowRedactionSteps(t *testing.T, messages []providers.Message) []string {
	t.Helper()
	steps := make([]string, len(messages))
	for i, m := range messages {
		body, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		steps[i] = string(body)
	}
	return steps
}

func TestCodexWorkflowStepsRedactEscapedStructuredSecrets(t *testing.T) {
	secret := "quote\"back\\line\n秘密"
	structured, _ := json.Marshal(map[string]any{secret: secret, "large": json.Number("18446744073709551615"), "nested": []any{secret}})
	messages := codexRedactionMessages(string(structured))
	messages[0].Content = "request " + secret
	messages[1].ToolCalls[0].Arguments = structured
	steps := workflowRedactionSteps(t, messages)
	before := append([]string(nil), steps...)
	clean, err := redactCodexWorkflowSteps(steps, []string{secret})
	if err != nil || len(clean) != len(steps) {
		t.Fatal(err)
	}
	decoded := make([]providers.Message, len(clean))
	for i, body := range clean {
		if json.Unmarshal([]byte(body), &decoded[i]) != nil {
			t.Fatal("invalid serialized message")
		}
	}
	if providers.ValidateMessages(decoded) != nil {
		t.Fatal("redaction broke tool pairing")
	}
	if decoded[0].Content != "request [REDACTED]" {
		t.Fatal("escaped credential remained in text")
	}
	for _, body := range []string{string(decoded[1].ToolCalls[0].Arguments), decoded[2].Content} {
		var object map[string]any
		decoder := json.NewDecoder(strings.NewReader(body))
		decoder.UseNumber()
		if decoder.Decode(&object) != nil || object["[REDACTED]"] != "[REDACTED]" || object["large"] != json.Number("18446744073709551615") || object["nested"].([]any)[0] != "[REDACTED]" {
			t.Fatal("structured credential or precision lost")
		}
	}
	if !reflect.DeepEqual(before, steps) {
		t.Fatal("input steps mutated")
	}
	// Exercise explicit Unicode escaping at both host-envelope and nested-tool
	// layers rather than relying only on encoding/json's default spelling.
	escaped := workflowRedactionSteps(t, codexRedactionMessages(`{"\u0073ecret":"\u0073ecret"}`))
	escaped[0] = `{"role":"user","content":"\u0073ecret"}`
	clean, err = redactCodexWorkflowSteps(escaped, []string{"secret"})
	if err != nil {
		t.Fatal(err)
	}
	var user, tool providers.Message
	if json.Unmarshal([]byte(clean[0]), &user) != nil || user.Content != "[REDACTED]" || json.Unmarshal([]byte(clean[2]), &tool) != nil || tool.Content != `{"[REDACTED]":"[REDACTED]"}` {
		t.Fatal("Unicode escaping hid credential")
	}
}

func TestCodexWorkflowStepsRejectAmbiguousSources(t *testing.T) {
	valid := workflowRedactionSteps(t, codexRedactionMessages(`{"ok":true}`))
	cases := map[string][]string{
		"nil": nil, "empty": {}, "null": {"null"},
		"unknown field":     {`{"role":"user","content":"ok","unknown":1}`},
		"duplicate":         {`{"role":"user","role":"user","content":"ok"}`},
		"escaped duplicate": {`{"role":"user","\u0072ole":"user","content":"ok"}`},
		"malformed":         {"{"}, "trailing": {`{"role":"user","content":"ok"} {}`},
		"utf8":    {"{\"role\":\"user\",\"content\":\"" + string([]byte{255}) + "\"}"},
		"size":    {strings.Repeat(" ", 1<<20) + `{}`},
		"depth":   {strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)},
		"pending": valid[:2], "orphan": valid[2:],
	}
	for _, content := range []string{`{bad}`, `{"secret":1,"\u0073ecret":2}`, `{} []`, strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65), `{"secret":1,"[REDACTED]":2}`} {
		cases["tool "+content] = workflowRedactionSteps(t, codexRedactionMessages(content))
	}
	for _, args := range []string{`{"x":1,"x":2}`, `{"x":1,"\u0078":2}`, `{"x":1} {}`, `{bad}`} {
		// Raw host source avoids Marshal normalizing or rejecting malformed JSON.
		steps := append([]string(nil), valid...)
		steps[1] = `{"role":"assistant","tool_calls":[{"id":"call","name":"delegate","arguments":` + args + `}]}`
		cases["arguments "+args] = steps
	}
	many := make([]string, 129)
	for i := range many {
		many[i] = `{"role":"user","content":"ok"}`
	}
	cases["steps count"] = many
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			before := append([]string(nil), steps...)
			out, err := redactCodexWorkflowSteps(steps, []string{"secret"})
			if !errors.Is(err, ErrAdmission) || out != nil {
				t.Fatal("ambiguous source admitted")
			}
			if len(steps) > 0 && !reflect.DeepEqual(before, steps) {
				t.Fatal("rejection mutated input")
			}
		})
	}
}

func TestCodexWorkflowStepsRejectCaseAliases(t *testing.T) {
	valid := workflowRedactionSteps(t, codexRedactionMessages(`{"ok":true}`))
	for _, name := range []string{"role", "content", "tool_calls", "tool_call_id", "tool_failed"} {
		index := 0
		if name == "tool_calls" {
			index = 1
		}
		if name == "tool_call_id" || name == "tool_failed" {
			index = 2
		}
		for _, duplicate := range []bool{false, true} {
			steps := append([]string(nil), valid...)
			var object map[string]json.RawMessage
			if json.Unmarshal([]byte(steps[index]), &object) != nil {
				t.Fatal("bad fixture")
			}
			value, exists := object[name]
			if !exists {
				value = json.RawMessage(`false`)
			}
			object[strings.ToUpper(name)] = value
			if !duplicate {
				delete(object, name)
			}
			body, _ := json.Marshal(object)
			steps[index] = string(body)
			if out, err := redactCodexWorkflowSteps(steps, nil); err == nil || out != nil {
				t.Fatal("message alias accepted", name, duplicate)
			}
		}
	}
	for _, name := range []string{"id", "name", "arguments"} {
		for _, duplicate := range []bool{false, true} {
			steps := append([]string(nil), valid...)
			var object map[string]any
			if json.Unmarshal([]byte(steps[1]), &object) != nil {
				t.Fatal("bad fixture")
			}
			call := object["tool_calls"].([]any)[0].(map[string]any)
			call[strings.ToUpper(name)] = call[name]
			if !duplicate {
				delete(call, name)
			}
			body, _ := json.Marshal(object)
			steps[1] = string(body)
			if out, err := redactCodexWorkflowSteps(steps, nil); err == nil || out != nil {
				t.Fatal("tool-call alias accepted", name, duplicate)
			}
		}
	}
	// Case-sensitive arbitrary keys inside arguments remain ordinary task data.
	messages := codexRedactionMessages(`{"ok":true}`)
	messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"Name":"upper","name":"lower"}`)
	if _, err := redactCodexWorkflowSteps(workflowRedactionSteps(t, messages), nil); err != nil {
		t.Fatal("task argument keys treated as protocol aliases", err)
	}
}
