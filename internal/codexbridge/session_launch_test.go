package codexbridge

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

const checkedConfig = `{"config":{"features":{"hooks":false},"mcp_servers":{},"plugins":{},"project_doc_max_bytes":0,"notify":[],"web_search":"disabled"}}`

func checkedResponses(cwd string, first int) []codexrpc.Envelope {
	return []codexrpc.Envelope{
		sessionResponse(strconv.Itoa(first), checkedConfig),
		sessionResponse(strconv.Itoa(first+1), `{"data":[],"nextCursor":null}`),
		{ID: marshal(first + 2), Result: marshal(map[string]any{"data": []any{map[string]any{"cwd": cwd, "skills": []any{}, "errors": []any{}}}})},
		{ID: marshal(first + 3), Result: marshal(map[string]any{"data": []any{map[string]any{"cwd": cwd, "hooks": []any{}, "errors": []any{}, "warnings": []any{}}}})},
	}
}

func checkedFixture(t *testing.T, cwd string, frames []codexrpc.Envelope) (*Session, *scriptedSessionWire) {
	t.Helper()
	w := &scriptedSessionWire{frames: frames, closed: make(chan struct{})}
	features := []string{"hooks"}
	s, err := NewCheckedSession(context.Background(), w, Options{Model: "gpt-5.6-sol", CWD: cwd}, features)
	if err != nil {
		t.Fatal(err)
	}
	features[0] = "changed_by_caller"
	if len(w.sent()) != 0 {
		t.Fatal("constructor sent traffic")
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, w
}

func TestCheckedSessionPrepareThenStreamRechecksWithoutReinitializing(t *testing.T) {
	cwd := t.TempDir()
	frames := []codexrpc.Envelope{sessionResponse("1", `{"userAgent":"fixture"}`), sessionNotice("remoteControl/status/changed", `{"status":"disabled"}`)}
	frames = append(frames, checkedResponses(cwd, 10)...)
	frames = append(frames, checkedResponses(cwd, 14)...)
	frames = append(frames, sessionPrefix()[1:]...)
	frames = append(frames, sessionFinal()...)
	s, w := checkedFixture(t, cwd, frames)
	if s.Prepare(context.Background()) != nil {
		t.Fatal("preparation failed")
	}
	for _, e := range w.sent() {
		if e.Method == "thread/start" || e.Method == "turn/start" {
			t.Fatal("preparation sent task traffic")
		}
	}
	var out []providers.Chunk
	if err := s.Stream(context.Background(), providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "fixture"}}}, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	initializes, checks := 0, 0
	for _, e := range w.sent() {
		if e.Method == "initialize" {
			initializes++
		}
		if e.Method == "config/read" {
			checks++
		}
	}
	if initializes != 1 || checks != 2 || len(out) == 0 {
		t.Fatal("handshake/check order incorrect")
	}
}

func TestCheckedSessionDeniesBeforeTaskInput(t *testing.T) {
	for _, mutation := range []string{"config", "tool", "skill", "hook", "remote", "request", "wrong_id", "unknown_notice"} {
		t.Run(mutation, func(t *testing.T) {
			cwd := t.TempDir()
			frames := append([]codexrpc.Envelope{sessionResponse("1", `{"userAgent":"fixture"}`)}, checkedResponses(cwd, 10)...)
			switch mutation {
			case "config":
				frames[1].Result = json.RawMessage(`{"config":{}}`)
			case "tool":
				frames[2].Result = json.RawMessage(`{"data":[{"tools":{"unsafe":{}}}]}`)
			case "skill":
				frames[3].Result = marshal(map[string]any{"data": []any{map[string]any{"cwd": cwd, "skills": []any{map[string]any{"path": "/fixture/SKILL.md", "enabled": true}}, "errors": []any{}}}})
			case "hook":
				frames[4].Result = marshal(map[string]any{"data": []any{map[string]any{"cwd": cwd, "hooks": []any{map[string]any{}}, "errors": []any{}, "warnings": []any{}}}})
			case "remote":
				frames[1] = sessionNotice("remoteControl/status/changed", `{"status":"connected"}`)
			case "request":
				frames[1] = codexrpc.Envelope{ID: marshal(99), Method: "item/tool/call", Params: marshal(map[string]any{})}
			case "wrong_id":
				frames[1].ID = marshal(99)
			case "unknown_notice":
				frames[1] = sessionNotice("unknown", `{}`)
			}
			s, w := checkedFixture(t, cwd, frames)
			if s.Stream(context.Background(), providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "must remain private"}}}, func(providers.Chunk) error { return nil }) == nil {
				t.Fatal("unsafe checks accepted")
			}
			for _, e := range w.sent() {
				if e.Method == "thread/start" || e.Method == "turn/start" || e.Result != nil {
					t.Fatal("failure released task data or answered request")
				}
			}
			select {
			case <-w.closed:
			default:
				t.Fatal("failed wire not closed")
			}
		})
	}
}

func TestCheckedSessionPrepareCancellation(t *testing.T) {
	s, w := checkedFixture(t, t.TempDir(), nil)
	w.block = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if s.Prepare(ctx) == nil {
		t.Fatal("cancelled prepare succeeded")
	}
	select {
	case <-w.closed:
	default:
		t.Fatal("cancel did not close")
	}
}

func TestCheckedSessionDoesNotReuseStalePrepare(t *testing.T) {
	cwd := t.TempDir()
	frames := append([]codexrpc.Envelope{sessionResponse("1", `{"userAgent":"fixture"}`)}, checkedResponses(cwd, 10)...)
	frames = append(frames, sessionResponse("14", `{"config":{}}`))
	s, w := checkedFixture(t, cwd, frames)
	if s.Prepare(context.Background()) != nil {
		t.Fatal("prepare failed")
	}
	if s.Stream(context.Background(), providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "private"}}}, func(providers.Chunk) error { return nil }) == nil {
		t.Fatal("stale preparation admitted input")
	}
	for _, e := range w.sent() {
		if e.Method == "thread/start" || e.Method == "turn/start" {
			t.Fatal("sent task after config changed")
		}
	}
}
