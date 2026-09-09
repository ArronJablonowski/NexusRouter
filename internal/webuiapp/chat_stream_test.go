package webuiapp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

var testStreamCursorKey = []byte("01234567890123456789012345678901")

func committedChatPage(t *testing.T) sessions.CommittedEventPage {
	t.Helper()
	now := time.Unix(100, 0).UTC()
	events := []runtime.Event{
		{Version: 1, ID: "event-1", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{Text: "private prompt", ProviderID: "secret-provider", ModelID: "secret-model"}},
		{Version: 1, ID: "event-2", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 2, Time: now, Kind: runtime.ToolStarted, TurnID: "turn", Data: runtime.Data{ToolCallID: "call", ToolName: "lookup", Effect: runtime.UncertainEffect, Text: "private arguments"}},
		{Version: 1, ID: "event-3", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 3, Time: now, Kind: runtime.ToolCompleted, TurnID: "turn", Data: runtime.Data{ToolCallID: "call", ToolName: "lookup", Effect: runtime.NoEffect, Text: "private tool result", Code: "private_failure_detail"}},
		{Version: 1, ID: "event-4", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 4, Time: now, Kind: runtime.TaskCompleted, Data: runtime.Data{Text: "private final"}},
	}
	items := make([]sessions.CommittedEvent, len(events))
	for index, event := range events {
		if event.Validate() != nil {
			t.Fatal("invalid fixture", event.Kind)
		}
		items[index] = sessions.CommittedEvent{Version: 1, Position: int64(index + 1), Event: event}
	}
	cursor, err := sessions.EncodeEventLogCursor(sessions.EventLogCursor{Version: 1, LastPosition: 4, LastEventID: "event-4", HighWaterPosition: 4, HighWaterEventID: "event-4"})
	if err != nil {
		t.Fatal(err)
	}
	return sessions.CommittedEventPage{Version: 1, Events: items, NextCursor: cursor}
}

func TestPrepareChatPageProjectsAllowlistedPairedLifecycle(t *testing.T) {
	page := committedChatPage(t)
	prepared, err := prepareChatPage(testStreamCursorKey, "chat", page)
	if err != nil || len(prepared.events) != 4 {
		t.Fatal("projection failed", len(prepared.events), err)
	}
	want := []contract.EventKind{contract.LifecycleEvent, contract.ToolChanged, contract.ToolChanged, contract.TaskTerminal}
	for index, event := range prepared.events {
		if event.Kind != want[index] || event.Validate() != nil || event.Revision != int64(index+1) {
			t.Fatal("invalid projected event", index, event)
		}
		body, _ := json.Marshal(event)
		for _, private := range []string{"private prompt", "secret-provider", "secret-model", "private arguments", "private tool result", "private_failure_detail", "private final"} {
			if strings.Contains(string(body), private) {
				t.Fatal("private runtime field escaped projection", private)
			}
		}
	}
	var started, completed contract.ToolChangedData
	if json.Unmarshal(prepared.events[1].Data, &started) != nil || json.Unmarshal(prepared.events[2].Data, &completed) != nil || started.ToolCallID != completed.ToolCallID || started.ToolName != completed.ToolName || started.State != "started" || completed.State != "completed" || completed.Code != "tool_error" {
		t.Fatal("tool lifecycle pair was not preserved", started, completed)
	}
}

func TestChatStreamCursorIsCanonicalAndChatBound(t *testing.T) {
	prepared, err := prepareChatPage(testStreamCursorKey, "chat", committedChatPage(t))
	if err != nil {
		t.Fatal(err)
	}
	last := prepared.events[len(prepared.events)-1].Cursor
	header := http.Header{"Last-Event-ID": []string{last}}
	ledger, err := parseChatStreamCursor(testStreamCursorKey, header, "chat")
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := sessions.ParseEventLogCursor(ledger)
	if err != nil || cursor.LastPosition != 4 || cursor.LastEventID != "event-4" {
		t.Fatal("cursor lost durable position", cursor, err)
	}
	if _, err := parseChatStreamCursor(testStreamCursorKey, header, "other"); err == nil {
		t.Fatal("cross-chat cursor accepted")
	}
	forgedBody, _ := base64.RawURLEncoding.DecodeString(last)
	otherSubject := sha256.Sum256([]byte("other"))
	copy(forgedBody[1:17], otherSubject[:16])
	forged := base64.RawURLEncoding.EncodeToString(forgedBody)
	if _, err := parseChatStreamCursor(testStreamCursorKey, http.Header{"Last-Event-ID": []string{forged}}, "other"); err == nil {
		t.Fatal("client rewrapped another chat's durable cursor")
	}
	header["Last-Event-ID"] = append(header["Last-Event-ID"], last)
	if _, err := parseChatStreamCursor(testStreamCursorKey, header, "chat"); err == nil {
		t.Fatal("duplicate cursor header accepted")
	}
	maxID := strings.Repeat("x", 128)
	maxLedger, err := sessions.EncodeEventLogCursor(sessions.EventLogCursor{Version: 1, LastPosition: 1, LastEventID: maxID, HighWaterPosition: 2, HighWaterEventID: strings.Repeat("y", 128)})
	if err != nil {
		t.Fatal(err)
	}
	maxCursor, err := encodeChatStreamCursor(testStreamCursorKey, strings.Repeat("c", 128), maxLedger)
	if err != nil || len(maxCursor) > contract.MaxCursorBytes {
		t.Fatal("valid durable cursor exceeded browser bound", len(maxCursor), err)
	}
}

func TestChatStreamResumeHasNoGapOrDuplicateAcrossFrozenPages(t *testing.T) {
	all := committedChatPage(t)
	firstCursor, err := sessions.EncodeEventLogCursor(sessions.EventLogCursor{Version: 1, LastPosition: 2, LastEventID: "event-2", HighWaterPosition: 4, HighWaterEventID: "event-4"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := prepareChatPage(testStreamCursorKey, "chat", sessions.CommittedEventPage{Version: 1, Events: all.Events[:2], NextCursor: firstCursor, HasMore: true})
	if err != nil || len(first.events) != 2 {
		t.Fatal("first replay page invalid", err, len(first.events))
	}
	ledger, err := parseChatStreamCursor(testStreamCursorKey, http.Header{"Last-Event-ID": []string{first.events[1].Cursor}}, "chat")
	if err != nil || ledger != firstCursor {
		t.Fatal("resume cursor did not preserve frozen ledger", err)
	}
	second, err := prepareChatPage(testStreamCursorKey, "chat", sessions.CommittedEventPage{Version: 1, Events: all.Events[2:], NextCursor: all.NextCursor})
	if err != nil || len(second.events) != 2 {
		t.Fatal("second replay page invalid", err, len(second.events))
	}
	var start, completion contract.ToolChangedData
	if json.Unmarshal(first.events[1].Data, &start) != nil || json.Unmarshal(second.events[0].Data, &completion) != nil || start.State != "started" || completion.State != "completed" || start.TaskID != completion.TaskID || start.TurnID != completion.TurnID || start.ToolCallID != completion.ToolCallID || start.ToolName != completion.ToolName {
		t.Fatal("split-page tool pair lost self-contained identity", start, completion)
	}
	seen := map[int64]bool{}
	for _, event := range append(first.events, second.events...) {
		if seen[event.Revision] {
			t.Fatal("duplicate durable revision", event.Revision)
		}
		seen[event.Revision] = true
	}
	for revision := int64(1); revision <= 4; revision++ {
		if !seen[revision] {
			t.Fatal("missing durable revision", revision)
		}
	}
}

type cancelFlushRecorder struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	limit   int
	flushes int
}

func authenticatedStreamRequest(ctx context.Context, cookie *http.Cookie) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7788/app/api/v1/chats/chat/events", nil).WithContext(ctx)
	request.AddCookie(cookie)
	return request
}

func (r *cancelFlushRecorder) Flush() {
	r.flushes++
	r.ResponseRecorder.Flush()
	if r.flushes >= r.limit {
		r.cancel()
	}
}

func TestAuthenticatedChatStreamUsesSeparateCapacity(t *testing.T) {
	handler := handlerFixture(t)
	page := committedChatPage(t)
	handler.reads.CommittedEvents = func(context.Context, sessions.EventLogOptions) (sessions.CommittedEventPage, error) { return page, nil }
	cookie, _ := authenticateBrowser(t, handler)
	for index := 0; index < cap(handler.slots); index++ {
		handler.slots <- struct{}{}
	}
	defer func() {
		for len(handler.slots) > 0 {
			<-handler.slots
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	request := authenticatedStreamRequest(ctx, cookie)
	response := &cancelFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, limit: 5}
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(response.Body.String(), "event: tool.changed") || len(handler.streamSlots) != 0 {
		t.Fatal("separate authenticated stream failed", response.Code, response.Body.String())
	}
}

func TestChatStreamPrevalidatesBeforeSSEHeaders(t *testing.T) {
	handler := handlerFixture(t)
	handler.reads.CommittedEvents = func(context.Context, sessions.EventLogOptions) (sessions.CommittedEventPage, error) {
		return sessions.CommittedEventPage{Version: 1, NextCursor: "invalid"}, nil
	}
	cookie, _ := authenticateBrowser(t, handler)
	request := authenticatedStreamRequest(context.Background(), cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || response.Header().Get("Content-Type") != "application/json" || strings.Contains(response.Body.String(), "event:") {
		t.Fatal("invalid page crossed SSE header boundary", response.Code, response.Header(), response.Body.String())
	}
}
