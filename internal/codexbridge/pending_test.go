package codexbridge

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func fixture(t *testing.T) (*Pending, providers.Request) {
	t.Helper()
	req := providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "Delegate this task."}}, Tools: []providers.Tool{{Name: "delegate", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	p, err := NewPending(req, "thread-1", "turn-1", "", CallRequest{ThreadID: "thread-1", TurnID: "turn-1", Namespace: "darwin", Tool: "delegate", CallID: "call-1", Arguments: json.RawMessage(`{"prompt":"Work","validation":"text"}`)})
	if err != nil {
		t.Fatal(err)
	}
	next := req
	next.Messages = append(next.Messages, providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{p.Proposal()}}, providers.Message{Role: "tool", ToolCallID: "call-1", Content: `{"untrusted_output":"Done"}`})
	return p, next
}

func TestPendingRoundTripAndSingleUse(t *testing.T) {
	p, next := fixture(t)
	out, err := p.Resume(next)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Success bool                          `json:"success"`
		Items   []struct{ Type, Text string } `json:"contentItems"`
	}
	if json.Unmarshal(out, &response) != nil || !response.Success || len(response.Items) != 1 || response.Items[0].Type != "inputText" || response.Items[0].Text != next.Messages[2].Content {
		t.Fatalf("incorrect response: %s", out)
	}
	if _, err = p.Resume(next); !errors.Is(err, ErrContinuation) {
		t.Fatal("accepted duplicate response")
	}
}

func TestPendingRejectsChangedContinuation(t *testing.T) {
	tests := map[string]func(*providers.Request){
		"model":          func(r *providers.Request) { r.Model = "another" },
		"history":        func(r *providers.Request) { r.Messages[0].Content = "changed" },
		"catalog":        func(r *providers.Request) { r.Tools[0].Description = "changed" },
		"schema":         func(r *providers.Request) { r.JSONSchema = json.RawMessage(`{}`) },
		"proposal text":  func(r *providers.Request) { r.Messages[1].Content = "changed" },
		"proposal args":  func(r *providers.Request) { r.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{}`) },
		"proposal id":    func(r *providers.Request) { r.Messages[1].ToolCalls[0].ID = "wrong" },
		"result id":      func(r *providers.Request) { r.Messages[2].ToolCallID = "wrong" },
		"result role":    func(r *providers.Request) { r.Messages[2].Role = "user" },
		"result call":    func(r *providers.Request) { r.Messages[2].ToolCalls = []providers.ToolCall{{ID: "nested"}} },
		"missing result": func(r *providers.Request) { r.Messages = r.Messages[:2] },
		"steering": func(r *providers.Request) {
			r.Messages = append(r.Messages, providers.Message{Role: "user", Content: "More"})
		},
		"oversized": func(r *providers.Request) { r.Messages[2].Content = strings.Repeat("x", maxExchangeBytes) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p, next := fixture(t)
			mutate(&next)
			if _, err := p.Resume(next); !errors.Is(err, ErrContinuation) {
				t.Fatal("accepted changed continuation")
			}
		})
	}
}

func TestPendingRejectsOtherToolAuthority(t *testing.T) {
	_, next := fixture(t)
	req := next
	req.Messages = req.Messages[:1]
	base := CallRequest{ThreadID: "thread-1", TurnID: "turn-1", Namespace: "darwin", Tool: "delegate", CallID: "call-1", Arguments: json.RawMessage(`{}`)}
	for name, mutate := range map[string]func(*CallRequest){
		"thread":    func(c *CallRequest) { c.ThreadID = "other" },
		"turn":      func(c *CallRequest) { c.TurnID = "other" },
		"builtin":   func(c *CallRequest) { c.Namespace = "functions"; c.Tool = "exec_command" },
		"namespace": func(c *CallRequest) { c.Namespace = "" },
		"unknown":   func(c *CallRequest) { c.Tool = "shell" },
		"empty id":  func(c *CallRequest) { c.CallID = "" },
		"array":     func(c *CallRequest) { c.Arguments = json.RawMessage(`[]`) },
		"null":      func(c *CallRequest) { c.Arguments = json.RawMessage(`null`) },
		"broken":    func(c *CallRequest) { c.Arguments = json.RawMessage(`{"secret":"never-print"`) },
		"large":     func(c *CallRequest) { c.Arguments = json.RawMessage(`{"a":"` + strings.Repeat("x", 16<<10) + `"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			call := base
			mutate(&call)
			if _, err := NewPending(req, "thread-1", "turn-1", "", call); !errors.Is(err, ErrContinuation) || strings.Contains(err.Error(), "never-print") {
				t.Fatal("invalid proposal not safely rejected")
			}
		})
	}
	if _, err := NewPending(next, "thread-1", "turn-1", "", base); !errors.Is(err, ErrContinuation) {
		t.Fatal("accepted previously used call ID")
	}
	req.Tools = append(req.Tools, req.Tools[0])
	if _, err := NewPending(req, "thread-1", "turn-1", "", base); !errors.Is(err, ErrContinuation) {
		t.Fatal("accepted duplicate tool catalog")
	}
}

func TestPendingOwnsProposalAndHistory(t *testing.T) {
	p, next := fixture(t)
	proposal := p.Proposal()
	proposal.Arguments[0] = '['
	// An attempted bad continuation must not change the retained original.
	bad := next
	bad.Model = "changed"
	_, _ = p.Resume(bad)
	if _, err := p.Resume(next); err != nil {
		t.Fatal(err)
	}
}

func TestPendingConcurrentResume(t *testing.T) {
	p, next := fixture(t)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := p.Resume(next); err == nil {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("got %d successful resolutions", successes.Load())
	}
}

func TestPendingPreflightAndOwnedInput(t *testing.T) {
	_, next := fixture(t)
	req := next
	req.Messages = req.Messages[:1]
	call := CallRequest{ThreadID: "t", TurnID: "u", Namespace: "darwin", Tool: "delegate", CallID: "new", Arguments: json.RawMessage(`{}`)}
	for name, mutate := range map[string]func(*providers.Request){
		"bad role":      func(r *providers.Request) { r.Messages = []providers.Message{{Role: "invalid"}} },
		"orphan result": func(r *providers.Request) { r.Messages = []providers.Message{{Role: "tool", ToolCallID: "orphan"}} },
		"unfinished call": func(r *providers.Request) {
			r.Messages = []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "a", Name: "delegate", Arguments: json.RawMessage(`{}`)}}}}
		},
		"large initial": func(r *providers.Request) {
			r.Messages = []providers.Message{{Role: "user", Content: strings.Repeat("x", maxExchangeBytes)}}
		},
		"encoding": func(r *providers.Request) { r.Model = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := req
			mutate(&bad)
			if _, err := NewPending(bad, "t", "u", "", call); !errors.Is(err, ErrContinuation) {
				t.Fatal("accepted malformed initial request")
			}
		})
	}
	p, err := NewPending(req, "t", "u", "", call)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages[0].Content = "mutated input"
	req.Tools[0].Parameters[0] = '['
	call.Arguments[0] = '['
	_, good := fixture(t)
	good.Messages[1].ToolCalls = []providers.ToolCall{p.Proposal()}
	good.Messages[2].ToolCallID = "new"
	if _, err = p.Resume(good); err != nil {
		t.Fatal("caller mutation altered snapshot:", err)
	}
}

func TestPendingRejectsLossyContinuation(t *testing.T) {
	for _, field := range []string{"history", "catalog", "result"} {
		t.Run(field, func(t *testing.T) {
			_, next := fixture(t)
			req := next
			req.Messages = req.Messages[:1]
			req.Messages[0].Content = "\ufffd"
			req.Tools[0].Description = "\ufffd"
			p, err := NewPending(req, "t", "u", "", CallRequest{ThreadID: "t", TurnID: "u", Namespace: "darwin", Tool: "delegate", CallID: "fresh", Arguments: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			next.Messages[1].ToolCalls = []providers.ToolCall{p.Proposal()}
			next.Messages[2].ToolCallID = "fresh"
			bad := string([]byte{0xff})
			switch field {
			case "history":
				next.Messages[0].Content = bad
			case "catalog":
				next.Tools[0].Description = bad
			case "result":
				next.Messages[2].Content = bad
			}
			if _, err := p.Resume(next); !errors.Is(err, ErrContinuation) {
				t.Fatal("accepted lossy continuation")
			}
		})
	}
}
