// Package toolbridge provides a per-run, host-owned native tool rendezvous.
// It is not a tool executor, approval authority or durable replay store.
package toolbridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

var ErrDenied = errors.New("native tool bridge denied")
var ErrUncertain = errors.New("native tool result unavailable; inspect host journal")

// Invoke is trusted host code. Before returning it must enforce the registered
// schema/policy/approval, commit tool lifecycle/effect records, redact Content,
// and join execution on cancellation. An uncertain effect is never retried.
// Calling a raw tool handler here does not satisfy this contract.
type Invoke func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error)

type entry struct {
	execution runtime.ToolExecution
	started   bool
	done      chan struct{}
	result    runtime.ToolResult
	err       error
}
type Bridge struct {
	ctx     context.Context
	cancel  context.CancelFunc
	token   string
	invoke  Invoke
	timeout time.Duration
	limit   int
	mu      sync.Mutex
	closed  bool
	halted  bool
	slot    chan struct{}
	calls   map[string]*entry
	wg      sync.WaitGroup
}

// New creates no listener or process. A native adapter must expose ServeHTTP only
// on its private loopback server and give Token only to its isolated child.
// The host must Close and join this bridge before closing journal/approval stores.
func New(ctx context.Context, limit int, timeout time.Duration, invoke Invoke) (*Bridge, error) {
	if ctx == nil || ctx.Err() != nil || limit < 1 || limit > 128 || timeout <= 0 || timeout > 15*time.Minute || invoke == nil {
		return nil, ErrDenied
	}
	child, cancel := context.WithCancel(ctx)
	return &Bridge{ctx: child, cancel: cancel, token: rand.Text(), invoke: invoke, timeout: timeout, limit: limit, calls: map[string]*entry{}, slot: make(chan struct{}, 1)}, nil
}
func (b *Bridge) Token() string { return b.token }
func label(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// Register is host-only and must follow verification of the actual model's tool
// proposal. The child cannot register calls, choose arguments, or assign task/
// turn/attempt identities. Exact registration retries are harmless; changed IDs
// conflict even if the previous execution failed. Arguments are copied.
func (b *Bridge) Register(x runtime.ToolExecution) error {
	if b == nil || !label(x.TaskID, 128) || !label(x.SessionID, 128) || !label(x.TurnID, 128) || !label(x.AttemptID, 128) || !label(x.Call.ID, 256) || !label(x.Call.Name, 64) || len(x.Call.Arguments) > 64<<10 || !wirejson.Unique(x.Call.Arguments) {
		return ErrDenied
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(x.Call.Arguments, &args) != nil || args == nil {
		return ErrDenied
	}
	x.Call.Arguments = append(json.RawMessage(nil), x.Call.Arguments...)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.ctx.Err() != nil {
		return ErrDenied
	}
	if old, ok := b.calls[x.Call.ID]; ok {
		if reflect.DeepEqual(old.execution, x) {
			return nil
		}
		return ErrDenied
	}
	if len(b.calls) >= b.limit {
		return ErrDenied
	}
	b.calls[x.Call.ID] = &entry{execution: x, done: make(chan struct{})}
	return nil
}
func (b *Bridge) Close() { b.cancel(); b.mu.Lock(); b.closed = true; b.mu.Unlock(); b.wg.Wait() }

func (b *Bridge) execute(ctx context.Context, id string) (runtime.ToolResult, error) {
	b.mu.Lock()
	e, ok := b.calls[id]
	if !ok || b.closed || b.ctx.Err() != nil || ctx.Err() != nil {
		b.mu.Unlock()
		return runtime.ToolResult{}, ErrDenied
	}
	first := !e.started
	if first {
		e.started = true
		b.wg.Add(1)
	}
	b.mu.Unlock()
	if first {
		defer b.wg.Done()
		call, cancel := context.WithTimeout(b.ctx, b.timeout)
		stop := context.AfterFunc(ctx, cancel)
		result, err := b.invokeRegistered(call, e.execution)
		stop()
		cancel()
		e.result, e.err = result, err
		close(e.done)
	} else {
		select {
		case <-e.done:
		case <-ctx.Done():
			return runtime.ToolResult{}, ErrUncertain
		case <-b.ctx.Done():
			return runtime.ToolResult{}, ErrUncertain
		}
	}
	return e.result, e.err
}

// Calls are serial within a run. A terminal result fences every later effect,
// including a different call already registered or waiting concurrently. Exact
// retries still retrieve their cached result; they never invoke the host twice.
func (b *Bridge) invokeRegistered(ctx context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
	select {
	case b.slot <- struct{}{}:
		defer func() { <-b.slot }()
	case <-ctx.Done():
		return runtime.ToolResult{}, ErrUncertain
	}
	b.mu.Lock()
	blocked := b.closed || b.halted || ctx.Err() != nil
	b.mu.Unlock()
	if blocked {
		return runtime.ToolResult{}, ErrDenied
	}
	x.Call.Arguments = append(json.RawMessage(nil), x.Call.Arguments...)
	result, err := invoke(ctx, b.invoke, x)
	if ctx.Err() != nil || err != nil || len(result.Content) > 1<<20 || !utf8.ValidString(result.Content) ||
		(result.Effect != runtime.NoEffect && result.Effect != runtime.ConfirmedEffect) ||
		(result.Recoverable && (!result.Failed || result.Effect != runtime.NoEffect)) ||
		(result.Failed && !result.Recoverable) {
		result, err = runtime.ToolResult{}, ErrUncertain
	}
	if err != nil || (result.EndToolUse && !result.Failed) {
		b.mu.Lock()
		b.halted = true
		b.mu.Unlock()
	}
	return result, err
}

func invoke(ctx context.Context, fn Invoke, x runtime.ToolExecution) (result runtime.ToolResult, err error) {
	defer func() {
		if recover() != nil {
			result = runtime.ToolResult{}
			err = ErrUncertain
		}
	}()
	return fn(ctx, x)
}

// HTTP accepts only a verified call ID. Canonical arguments remain host-owned;
// native retries may retrieve a completed result but never execute again. Memory
// replay ends with this bridge: restart requires canonical host reconciliation.
func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	deny := func(code int) { http.Error(w, "native tool unavailable", code) }
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() || r.Method != "POST" || r.URL.Path != "/v1/tool" || r.URL.RawQuery != "" || r.Header.Get("Origin") != "" || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+b.token)) != 1 {
		deny(403)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		deny(400)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if err != nil || len(body) > 1024 || !wirejson.Unique(body) {
		deny(400)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 1 {
		deny(400)
		return
	}
	var id string
	if json.Unmarshal(fields["call_id"], &id) != nil || !label(id, 256) {
		deny(400)
		return
	}
	result, err := b.execute(r.Context(), id)
	if err != nil {
		deny(409)
		return
	}
	// Only host-redacted tool content and trusted failure state cross back.
	output := struct {
		Content    string `json:"content"`
		Failed     bool   `json:"failed"`
		EndToolUse bool   `json:"end_tool_use"`
	}{result.Content, result.Failed, result.EndToolUse && !result.Failed}
	var data bytes.Buffer
	if json.NewEncoder(&data).Encode(output) != nil {
		deny(500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data.Bytes())
}
