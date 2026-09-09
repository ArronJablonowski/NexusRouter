package webuiapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestLiveTextHubBackpressureDetachesWithoutBlockingPublisher(t *testing.T) {
	hub := NewLiveTextHub()
	stream, disconnect := hub.subscribe("chat")
	for index := 0; index < liveTextQueueDepth+1; index++ {
		hub.Publish("task", "chat", "chunk")
	}
	for index := 0; index < liveTextQueueDepth; index++ {
		if chunk, ok := <-stream; !ok || chunk.text != "chunk" || chunk.task != "task" {
			t.Fatal("queued text lost before bounded capacity", index, chunk, ok)
		}
	}
	if _, ok := <-stream; ok {
		t.Fatal("slow observer was not detached")
	}
	disconnect()
	done := make(chan struct{})
	go func() { hub.Publish("task", "chat", strings.Repeat("safe ", 1<<17)); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnected observer stalled publisher")
	}
}

func TestLiveTextHubSplitsUTF8WithinBound(t *testing.T) {
	hub := NewLiveTextHub()
	stream, disconnect := hub.subscribe("chat")
	defer disconnect()
	text := strings.Repeat("é", liveTextChunkBytes/2+1)
	hub.Publish("task", "chat", text)
	first, second := <-stream, <-stream
	if first.text+second.text != text || len(first.text) > liveTextChunkBytes || len(second.text) > liveTextChunkBytes {
		t.Fatal("UTF-8 split was lossy or unbounded")
	}
}

func TestInterleavedTaskDeltasRetainIdentityAcrossOutOfOrderTerminals(t *testing.T) {
	hub := NewLiveTextHub()
	stream, disconnect := hub.subscribe("chat")
	defer disconnect()
	hub.Publish("task-a", "chat", "a1")
	hub.Publish("task-b", "chat", "b1")
	terminal := map[string]bool{}
	for _, want := range []string{"task-a", "task-b"} {
		chunk := <-stream
		delta, visible := projectLiveChatDelta("chat", terminal, chunk)
		if !visible || !strings.Contains(string(delta.Data), `"task_id":"`+want+`"`) {
			t.Fatal("interleaved delta lost task identity", want, delta)
		}
	}
	terminal["task-b"] = true
	hub.Publish("task-a", "chat", "a2")
	hub.Publish("task-b", "chat", "b2")
	afterA, visibleA := projectLiveChatDelta("chat", terminal, <-stream)
	afterB, visibleB := projectLiveChatDelta("chat", terminal, <-stream)
	if !visibleA || visibleB || !strings.Contains(string(afterA.Data), `"task_id":"task-a"`) || afterB.Kind != "" {
		t.Fatal("out-of-order terminal did not isolate task buffers", afterA, visibleA, afterB, visibleB)
	}
	terminal["task-a"] = true
	if _, visible := projectLiveChatDelta("chat", terminal, liveTextChunk{task: "task-a", text: "late"}); visible {
		t.Fatal("post-terminal task delta remained visible")
	}
}

func TestChatStreamEmitsProvisionalLiveTextWithoutResumeID(t *testing.T) {
	handler := handlerFixture(t)
	handler.liveText = NewLiveTextHub()
	ready := make(chan struct{}, 1)
	zero, err := sessions.EncodeEventLogCursor(sessions.EventLogCursor{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	handler.reads.CommittedEvents = func(context.Context, sessions.EventLogOptions) (sessions.CommittedEventPage, error) {
		select {
		case ready <- struct{}{}:
		default:
		}
		return sessions.CommittedEventPage{Version: 1, NextCursor: zero}, nil
	}
	cookie, _ := authenticateBrowser(t, handler)
	ctx, cancel := context.WithCancel(context.Background())
	response := &cancelFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, limit: 2}
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, authenticatedStreamRequest(ctx, cookie))
		close(done)
	}()
	<-ready
	handler.liveText.Publish("task", "chat", "redacted live text")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("live stream did not observe text")
	}
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "event: chat.delta") || !strings.Contains(body, "redacted live text") || strings.Contains(body, "id: ") {
		t.Fatal("invalid provisional SSE frame", response.Code, body)
	}
}

func TestChatStreamRejectsNonNoBodyGET(t *testing.T) {
	handler := handlerFixture(t)
	cookie, _ := authenticateBrowser(t, handler)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7788/app/api/v1/chats/chat/events", strings.NewReader(""))
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("Content-Type") != "application/json" {
		t.Fatal("GET with non-NoBody reader accepted", response.Code)
	}
}

func TestDurableTerminalSuppressesLateDeltaAndDrivesRestartReconcile(t *testing.T) {
	handler := handlerFixture(t)
	handler.liveText = NewLiveTextHub()
	page := committedChatPage(t)
	reads := 0
	handler.reads.CommittedEvents = func(_ context.Context, options sessions.EventLogOptions) (sessions.CommittedEventPage, error) {
		reads++
		if options.After == "" {
			// Publication happens after subscription and after the terminal commit
			// exists, modeling the redactor tail flushed by TaskCompleted.
			handler.liveText.Publish("task", "chat", "late tail")
			return page, nil
		}
		return sessions.CommittedEventPage{Version: 1, NextCursor: page.NextCursor}, nil
	}
	cookie, _ := authenticateBrowser(t, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedStreamRequest(ctx, cookie))
	body := response.Body.String()
	if reads < 2 || !strings.Contains(body, "event: task.terminal") || strings.Contains(body, "late tail") || strings.Contains(body, "event: chat.delta") {
		t.Fatal("terminal/restart reconciliation ordering failed", reads, body)
	}
}
