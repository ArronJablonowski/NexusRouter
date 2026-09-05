package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type chatLiveOutput struct {
	mu      sync.Mutex
	text    strings.Builder
	visible chan struct{}
	once    sync.Once
	fail    bool
}

func TestChatLiveHTTPSteeringWhileProviderBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	complete := make(chan struct{})
	var calls atomic.Int32
	requests := make(chan []providers.Message, 4)
	svc := chatLiveFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "fixture", 400)
			return
		}
		requests <- request.Messages
		if calls.Add(1) == 1 {
			chatLiveChunk(t, w, "LIVE_PREFIX "+strings.Repeat("safe ", 1000), false)
			select {
			case <-complete:
			case <-r.Context().Done():
				return
			}
			chatLiveChunk(t, w, " first turn", true)
		} else {
			chatLiveChunk(t, w, "GUIDED_FINAL", true)
		}
	})
	out := &chatLiveOutput{visible: make(chan struct{})}
	lines := make(chan chatLine, 2)
	lines <- chatLine{Text: "write a response"}
	done := make(chan int, 1)
	go func() {
		done <- runChatSession(ctx, app.Request{ModelID: "chat"}, chatHooks{RunLive: svc.RunLiveStream, Steer: svc.SteerTask}, lines, nil, out)
	}()
	select {
	case <-out.visible:
	case <-ctx.Done():
		t.Fatal("missing live prefix")
	}
	lines <- chatLine{Text: "/steer include the moon"}
	chatLiveWait(t, ctx, out, "Guidance queued:")
	close(complete)
	close(lines)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal(code)
		}
	case <-ctx.Done():
		t.Fatal("steered live run did not join")
	}
	if calls.Load() != 2 || !strings.Contains(out.String(), "GUIDED_FINAL") || !strings.Contains(out.String(), "[guidance applied]") {
		t.Fatal("live steering not executed", calls.Load())
	}
	<-requests
	second := <-requests
	found := false
	for _, message := range second {
		if message.Role == "user" && strings.Contains(message.Content, "include the moon") {
			found = true
		}
	}
	if !found {
		t.Fatal("queued steering missing from next provider turn")
	}
}

func chatLiveWait(t *testing.T, ctx context.Context, out *chatLiveOutput, part string) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !strings.Contains(out.String(), part) {
		select {
		case <-ctx.Done():
			t.Fatal("missing chat output", part)
		case <-ticker.C:
		}
	}
}

func TestChatLiveHTTPFailedPartialPreservesSuccessfulContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var calls, feedback atomic.Int32
	requests := make(chan []providers.Message, 4)
	svc := chatLiveFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "fixture", 400)
			return
		}
		requests <- request.Messages
		switch calls.Add(1) {
		case 1:
			chatLiveChunk(t, w, "FIRST_OK", true)
		case 2:
			chatLiveChunk(t, w, "LIVE_PREFIX "+strings.Repeat("safe ", 1000), false)
			_, _ = io.WriteString(w, "{\"error\":\"fixture failure\"}\n")
			w.(http.Flusher).Flush()
		default:
			chatLiveChunk(t, w, "THIRD_OK", true)
		}
	})
	out := &chatLiveOutput{visible: make(chan struct{})}
	lines := make(chan chatLine, 4)
	lines <- chatLine{Text: "first prompt"}
	done := make(chan int, 1)
	hooks := chatHooks{RunLive: svc.RunLiveStream, Feedback: func(context.Context, string, bool, float64) error { feedback.Add(1); return nil }}
	go func() { done <- runChatSession(ctx, app.Request{ModelID: "chat"}, hooks, lines, nil, out) }()
	chatLiveWait(t, ctx, out, "\n[task completed]\n")
	lines <- chatLine{Text: "failed prompt"}
	chatLiveWait(t, ctx, out, "Task did not complete successfully")
	lines <- chatLine{Text: "/feedback accepted 0"}
	chatLiveWait(t, ctx, out, "Feedback requires a successful answer")
	lines <- chatLine{Text: "third prompt"}
	chatLiveWait(t, ctx, out, "THIRD_OK")
	close(lines)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal(code)
		}
	case <-ctx.Done():
		t.Fatal("continuation did not join")
	}
	if calls.Load() != 3 || feedback.Load() != 0 || strings.Count(out.String(), "\n[task completed]\n") != 2 || strings.Count(out.String(), "LIVE_PREFIX") != 1 {
		t.Fatal("failed partial was accepted or feedback retargeted")
	}
	<-requests
	<-requests
	third := <-requests
	if len(third) != 3 || third[0].Content != "first prompt" || third[1].Role != "assistant" || third[1].Content != "FIRST_OK" || third[2].Content != "third prompt" {
		t.Fatal("failed partial polluted continuation")
	}
}

func (o *chatLiveOutput) Write(body []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if strings.Contains(string(body), "LIVE_PREFIX") {
		o.once.Do(func() { close(o.visible) })
		if o.fail {
			return 0, errors.New("fixture output unavailable")
		}
	}
	o.text.Write(body)
	return len(body), nil
}

func (o *chatLiveOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.text.String()
}

func chatLiveFixture(t *testing.T, handler http.HandlerFunc) *app.Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "live.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 16384, EstimatedCost: &zero, RAMBytes: 1}}
	svc, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func chatLiveChunk(t *testing.T, w http.ResponseWriter, text string, done bool) {
	t.Helper()
	frame := map[string]any{"message": map[string]string{"content": text}, "done": done}
	if done {
		frame["done_reason"] = "stop"
	}
	body, err := json.Marshal(frame)
	if err != nil {
		t.Error(err)
		return
	}
	// Exercise UTF-8 split across HTTP writes without corrupting the JSON frame.
	cut := strings.Index(string(body), "界")
	if cut >= 0 {
		_, _ = w.Write(body[:cut+1])
		w.(http.Flusher).Flush()
		_, _ = w.Write(body[cut+1:])
	} else {
		_, _ = w.Write(body)
	}
	_, _ = io.WriteString(w, "\n")
	w.(http.Flusher).Flush()
}

func TestChatLiveHTTPPrefixBeforeCompletionAndSafeOutput(t *testing.T) {
	const secret = "chat-fixture-secret-split-at-stream-boundary"
	t.Setenv("DARWIN_API_TOKEN", secret)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	complete := make(chan struct{})
	svc := chatLiveFixture(t, func(w http.ResponseWriter, r *http.Request) {
		chatLiveChunk(t, w, "LIVE_PREFIX "+strings.Repeat("safe ", 1000), false)
		chatLiveChunk(t, w, secret[:19], false)
		chatLiveChunk(t, w, secret[19:]+" 世界\x1b[31m colored\x1b[0m", false)
		select {
		case <-complete:
		case <-r.Context().Done():
			return
		}
		chatLiveChunk(t, w, " FINAL_SUFFIX", true)
	})
	out := &chatLiveOutput{visible: make(chan struct{})}
	lines := make(chan chatLine, 1)
	lines <- chatLine{Text: "write a response"}
	close(lines)
	done := make(chan int, 1)
	go func() {
		done <- runChatSession(ctx, app.Request{ModelID: "chat"}, chatHooks{RunLive: svc.RunLiveStream}, lines, nil, out)
	}()
	select {
	case <-out.visible:
	case <-ctx.Done():
		t.Fatal("prefix withheld until completion")
	}
	select {
	case <-done:
		t.Fatal("chat completed before provider final frame")
	default:
	}
	close(complete)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal(code)
		}
	case <-ctx.Done():
		t.Fatal("live chat did not join")
	}
	rendered := out.String()
	if strings.Count(rendered, "LIVE_PREFIX") != 1 || strings.Count(rendered, "FINAL_SUFFIX") != 1 || strings.Contains(rendered, secret[:19]) || strings.Contains(rendered, secret[19:]) || !strings.Contains(rendered, "[REDACTED]") || !strings.Contains(rendered, "世界") || !utf8.ValidString(rendered) || strings.Contains(rendered, "\x1b") || !strings.Contains(rendered, "\n[task completed]\n") {
		t.Fatal("live output was duplicated, unsafe, or incomplete")
	}
}

func TestChatLiveHTTPFailureAfterPartialOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	complete := make(chan struct{})
	svc := chatLiveFixture(t, func(w http.ResponseWriter, r *http.Request) {
		chatLiveChunk(t, w, "LIVE_PREFIX "+strings.Repeat("safe ", 1000), false)
		select {
		case <-complete:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "{\"error\":\"fixture provider failure\"}\n")
		w.(http.Flusher).Flush()
	})
	out := &chatLiveOutput{visible: make(chan struct{})}
	lines := make(chan chatLine, 1)
	lines <- chatLine{Text: "write a response"}
	close(lines)
	done := make(chan int, 1)
	go func() {
		done <- runChatSession(ctx, app.Request{ModelID: "chat"}, chatHooks{RunLive: svc.RunLiveStream}, lines, nil, out)
	}()
	select {
	case <-out.visible:
	case <-ctx.Done():
		t.Fatal("missing partial output")
	}
	close(complete)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("failed provider did not join")
	}
	rendered := out.String()
	if strings.Count(rendered, "LIVE_PREFIX") != 1 || !strings.Contains(rendered, "Task did not complete successfully") || strings.Contains(rendered, "fixture provider failure") || strings.Contains(rendered, "\n[task completed]\n") {
		t.Fatal("failed partial response lacked safe warning")
	}
}

func TestChatLiveHTTPOutputFailureCancelsProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	providerCanceled := make(chan struct{})
	svc := chatLiveFixture(t, func(w http.ResponseWriter, r *http.Request) {
		chatLiveChunk(t, w, "LIVE_PREFIX "+strings.Repeat("safe ", 1000), false)
		select {
		case <-r.Context().Done():
			close(providerCanceled)
		case <-ctx.Done():
		}
	})
	out := &chatLiveOutput{visible: make(chan struct{}), fail: true}
	lines := make(chan chatLine, 1)
	lines <- chatLine{Text: "write a response"}
	close(lines)
	done := make(chan int, 1)
	go func() {
		done <- runChatSession(ctx, app.Request{ModelID: "chat"}, chatHooks{RunLive: svc.RunLiveStream}, lines, nil, out)
	}()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatal(code)
		}
	case <-ctx.Done():
		t.Fatal("output failure did not join owned run")
	}
	select {
	case <-providerCanceled:
	case <-ctx.Done():
		t.Fatal("output failure did not cancel HTTP provider")
	}
}
