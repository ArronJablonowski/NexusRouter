package codexbridge

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestPendingSteeredOwnershipAndSingleUse(t *testing.T) {
	p, next := fixture(t)
	next.Messages = append(next.Messages, providers.Message{Role: "user", Content: "private guidance"})
	if _, err := p.Resume(next); err != ErrContinuation {
		t.Fatal("legacy resume admitted guidance")
	}
	response, guidance, err := p.ResumeSteered(next)
	if err != nil || len(guidance) != 1 || guidance[0].Content != "private guidance" || bytes.Contains(response, []byte("private guidance")) {
		t.Fatal("guidance mixed with tool result", err)
	}
	guidance[0].Content = "changed"
	if next.Messages[3].Content != "private guidance" {
		t.Fatal("aliased caller messages")
	}
	if _, _, err := p.ResumeSteered(next); err != ErrContinuation {
		t.Fatal("replay admitted")
	}
	if _, err := p.Resume(next); err != ErrContinuation {
		t.Fatal("legacy replay admitted")
	}
	p, next = fixture(t)
	if _, g, err := p.ResumeSteered(next); err != nil || len(g) != 0 {
		t.Fatal("zero guidance", err)
	}
}

func TestPendingSteeredRejectsChangedCorrespondence(t *testing.T) {
	for name, mutate := range map[string]func(*providers.Request){
		"role":           func(r *providers.Request) { r.Messages[3].Role = "system" },
		"tool id":        func(r *providers.Request) { r.Messages[3].ToolCallID = "call" },
		"failed":         func(r *providers.Request) { r.Messages[3].ToolFailed = true },
		"calls":          func(r *providers.Request) { r.Messages[3].ToolCalls = []providers.ToolCall{{ID: "call"}} },
		"empty":          func(r *providers.Request) { r.Messages[3].Content = "" },
		"blank":          func(r *providers.Request) { r.Messages[3].Content = " \n\t" },
		"invalid utf8":   func(r *providers.Request) { r.Messages[3].Content = string([]byte{255}) },
		"large guidance": func(r *providers.Request) { r.Messages[3].Content = strings.Repeat("x", (64<<10)+1) },
		"too many": func(r *providers.Request) {
			for len(r.Messages) < 36 {
				r.Messages = append(r.Messages, providers.Message{Role: "user", Content: "x"})
			}
		},
		"prefix":          func(r *providers.Request) { r.Messages[0].Content = "changed" },
		"catalog":         func(r *providers.Request) { r.Tools[0].Description = "changed" },
		"proposal":        func(r *providers.Request) { r.Messages[1].Content = "changed" },
		"model":           func(r *providers.Request) { r.Model = "changed" },
		"result":          func(r *providers.Request) { r.Messages[2].ToolCallID = "changed" },
		"aggregate limit": func(r *providers.Request) { r.Messages[2].Content = strings.Repeat("x", maxExchangeBytes) },
	} {
		t.Run(name, func(t *testing.T) {
			p, next := fixture(t)
			next.Messages = append(next.Messages, providers.Message{Role: "user", Content: "guidance"})
			mutate(&next)
			if response, g, err := p.ResumeSteered(next); err != ErrContinuation || response != nil || g != nil {
				t.Fatal("invalid accepted", err)
			}
			_, valid := fixture(t)
			if _, _, err := p.ResumeSteered(valid); err != nil {
				t.Fatal("rejection consumed pending", err)
			}
		})
	}
}

func TestPendingSteeredExactLimits(t *testing.T) {
	p, next := fixture(t)
	for i := 0; i < 32; i++ {
		next.Messages = append(next.Messages, providers.Message{Role: "user", Content: strings.Repeat("x", 64<<10)})
	}
	if _, g, err := p.ResumeSteered(next); err != nil || len(g) != 32 {
		t.Fatal("exact bounds rejected", err)
	}
	var absent *Pending
	if _, _, err := absent.ResumeSteered(next); err != ErrContinuation {
		t.Fatal(err)
	}
}
