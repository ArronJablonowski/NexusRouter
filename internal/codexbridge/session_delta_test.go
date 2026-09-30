package codexbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestSessionDeltasVerifiedWithoutDuplicateText(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		frames := append(sessionPrefix(), sessionFinal()[0])
		frames = append(frames, sessionNotice("item/agentMessage/delta", `{"threadId":"thread-1","turnId":"turn-1","itemId":"answer-1","delta":"Reviewed "}`))
		frames = append(frames, sessionNotice("item/agentMessage/delta", `{"threadId":"thread-1","turnId":"turn-1","itemId":"answer-1","delta":"local result."}`))
		completion := sessionFinal()[1]
		if corrupt {
			completion = sessionNotice("item/completed", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"answer-1","text":"Different text","phase":"final_answer"}}`)
		}
		frames = append(frames, completion, sessionFinal()[2])
		s, _, req := newSessionFixture(t, frames)
		var chunks []providers.Chunk
		err := s.Stream(context.Background(), req, collectSession(&chunks))
		text := ""
		finished := false
		for _, c := range chunks {
			text += c.Text
			finished = finished || c.Done
		}
		if text != "Reviewed local result." {
			t.Fatalf("duplicate or lost deltas: %q", text)
		}
		if corrupt {
			var f *providers.Failure
			if !errors.As(err, &f) || !f.Partial || finished {
				t.Fatalf("mismatched completion succeeded: %v %+v", err, chunks)
			}
		} else if err != nil || !finished || len(chunks) != 3 {
			t.Fatalf("completion: %v %+v", err, chunks)
		}
	}
}

func TestSessionRejectsPrematureToolAndInitialization(t *testing.T) {
	for _, frames := range [][]codexrpc.Envelope{
		{sessionResponse("1", `null`)},
		{sessionResponse("1", `{"userAgent":"a","USERAGENT":"b"}`)},
		append(append(sessionPrefix(), sessionFinal()[0]), sessionTool()...),
		{sessionPrefix()[0], sessionNotice("item/started", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"dynamicToolCall","id":"call-1"}}`)},
	} {
		s, _, req := newSessionFixture(t, frames)
		if err := s.Stream(context.Background(), req, func(c providers.Chunk) error {
			if c.Done {
				t.Fatal("premature success")
			}
			return nil
		}); err == nil {
			t.Fatal("accepted premature protocol")
		}
	}
}

func TestSessionReasoningIsNotAnswerText(t *testing.T) {
	frames := append(sessionPrefix(), sessionNotice("item/started", `{"threadId":"thread-1","turnId":"turn-1","item":{"id":"reason-1","type":"reasoning"}}`))
	frames = append(frames, sessionNotice("item/reasoning/textDelta", `{"threadId":"thread-1","turnId":"turn-1","itemId":"reason-1","delta":"private reasoning fixture"}`))
	frames = append(frames, sessionNotice("item/completed", `{"threadId":"thread-1","turnId":"turn-1","item":{"id":"reason-1","type":"reasoning"}}`))
	frames = append(frames, sessionNotice("thread/status/changed", `{"threadId":"thread-1","status":{"type":"active","activeFlags":[]}}`))
	frames = append(frames, sessionFinal()...)
	s, _, req := newSessionFixture(t, frames)
	text := ""
	if err := s.Stream(context.Background(), req, func(c providers.Chunk) error { text += c.Text; return nil }); err != nil || text != "Reviewed local result." {
		t.Fatalf("reasoning leaked or failed: %v %q", err, text)
	}
}
