package toolbridge

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func proposal() runtime.ToolExecution {
	return runtime.ToolExecution{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt", Call: providers.ToolCall{ID: "call", Name: "lookup", Arguments: []byte(`{"path":"safe"}`)}}
}
func bridge(t *testing.T, fn Invoke) *Bridge {
	t.Helper()
	b, err := New(context.Background(), 2, time.Second, fn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	if err = b.Register(proposal()); err != nil {
		t.Fatal(err)
	}
	return b
}
func request(b *Bridge, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://localhost/v1/tool", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer "+b.Token())
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	return w
}
func TestHTTPRejectsUnverifiedAuthority(t *testing.T) {
	var calls atomic.Int32
	b := bridge(t, func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
		calls.Add(1)
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	})
	for _, body := range []string{`{"call_id":"unknown"}`, `{"Call_ID":"call"}`, `{"call_id":"call","call_id":"call"}`, `{"call_id":"call","arguments":{}}`, `{"call_id":null}`, `[]`, strings.Repeat("x", 1025)} {
		if w := request(b, body); w.Code == 200 {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, kind := range []string{"token", "origin", "remote", "method", "query", "content_type", "duplicate_auth"} {
		t.Run(kind, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://localhost/v1/tool", strings.NewReader(`{"call_id":"call"}`))
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Authorization", "Bearer "+b.Token())
			r.Header.Set("Content-Type", "application/json")
			switch kind {
			case "token":
				r.Header.Set("Authorization", "Bearer wrong")
			case "origin":
				r.Header.Set("Origin", "https://example.org")
			case "remote":
				r.RemoteAddr = "192.168.1.2:1234"
			case "method":
				r.Method = "GET"
			case "query":
				r.URL.RawQuery = "extra=1"
			case "content_type":
				r.Header.Del("Content-Type")
			case "duplicate_auth":
				r.Header.Add("Authorization", "Bearer "+b.Token())
			}
			w := httptest.NewRecorder()
			b.ServeHTTP(w, r)
			if w.Code == 200 {
				t.Fatal("accepted")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("unverified execution")
	}
}
func TestConcurrentReplayAndBinding(t *testing.T) {
	var calls atomic.Int32
	b := bridge(t, func(_ context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
		calls.Add(1)
		if x.TaskID != "task" || x.SessionID != "session" || x.TurnID != "turn" || x.AttemptID != "attempt" || string(x.Call.Arguments) != `{"path":"safe"}` {
			t.Error("binding changed")
		}
		x.Call.Arguments[0] = 'x'
		return runtime.ToolResult{Content: "redacted result", Effect: runtime.NoEffect}, nil
	})
	x := proposal()
	if err := b.Register(x); err != nil {
		t.Fatal(err)
	}
	x.Call.Arguments[0] = 'x'
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request(b, `{"call_id":"call"}`)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "redacted result") {
				t.Errorf("result %d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("executions %d", calls.Load())
	}
	if err := b.Register(proposal()); err != nil {
		t.Fatal("callback mutated canonical arguments", err)
	}
	x = proposal()
	x.TaskID = "other"
	if b.Register(x) == nil {
		t.Fatal("changed binding accepted")
	}
	x = proposal()
	x.Call.ID = "second"
	if err := b.Register(x); err != nil {
		t.Fatal(err)
	}
	x.Call.ID = "third"
	if b.Register(x) == nil {
		t.Fatal("limit ignored")
	}
}
func TestUncertainNeverReexecutesOrLeaks(t *testing.T) {
	for _, kind := range []string{"error", "panic", "uncertain", "invalid_effect", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			b := bridge(t, func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
				calls.Add(1)
				r := runtime.ToolResult{Content: "private detail", Effect: runtime.NoEffect}
				switch kind {
				case "error":
					return r, errors.New("private error")
				case "panic":
					panic("private panic")
				case "uncertain":
					r.Effect = runtime.UncertainEffect
				case "invalid_effect":
					r.Effect = ""
				case "oversize":
					r.Content = strings.Repeat("x", (1<<20)+1)
				}
				return r, nil
			})
			for range 2 {
				w := request(b, `{"call_id":"call"}`)
				if w.Code != 409 || strings.Contains(w.Body.String(), "private") {
					t.Fatalf("leaked result %d %s", w.Code, w.Body.String())
				}
			}
			if calls.Load() != 1 {
				t.Fatal("reexecuted uncertain effect")
			}
		})
	}
}
func TestCloseCancelsAndJoins(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	b := bridge(t, func(ctx context.Context, _ runtime.ToolExecution) (runtime.ToolResult, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = b.execute(context.Background(), "call") }()
	<-entered
	closed := make(chan struct{})
	go func() { b.Close(); close(closed) }()
	<-canceled
	select {
	case <-closed:
		t.Error("close returned before invocation joined")
	default:
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not join")
	}
	<-done
	if b.Register(proposal()) == nil {
		t.Fatal("registered after close")
	}
}
func TestRequestCancellationIsTerminal(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int32
	b := bridge(t, func(ctx context.Context, _ runtime.ToolExecution) (runtime.ToolResult, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		return runtime.ToolResult{Content: "late", Effect: runtime.NoEffect}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.execute(ctx, "call"); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if _, err := b.execute(context.Background(), "call"); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("reexecuted canceled call")
	}
}
