package webuiapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

const (
	chatStreamPageLimit = 100
	chatStreamMaxEvents = 1000
	chatStreamMaxBytes  = 1 << 20
	chatStreamReadTime  = 5 * time.Second
	chatStreamLife      = 30 * time.Second
	chatStreamPoll      = 250 * time.Millisecond
)

var browserStreamID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func chatEventsID(base, path string) string {
	prefix, suffix := base+"/api/v1/chats/", "/events"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if !browserStreamID.MatchString(id) || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func encodeChatStreamCursor(key []byte, chat, ledger string) (string, error) {
	if len(key) != 32 || !browserStreamID.MatchString(chat) || ledger == "" {
		return "", sessions.ErrEventLog
	}
	cursor, err := sessions.ParseEventLogCursor(ledger)
	if err != nil {
		return "", sessions.ErrEventLog
	}
	var body bytes.Buffer
	body.WriteByte(1)
	subject := sha256.Sum256([]byte(chat))
	body.Write(subject[:16])
	writeCursorNumber(&body, uint64(cursor.LastPosition))
	writeCursorNumber(&body, uint64(cursor.HighWaterPosition))
	writeCursorString(&body, cursor.LastEventID)
	writeCursorString(&body, cursor.HighWaterEventID)
	mac := hmac.New(sha256.New, key)
	mac.Write(body.Bytes())
	body.Write(mac.Sum(nil)[:16])
	encoded := base64.RawURLEncoding.EncodeToString(body.Bytes())
	if len(encoded) > contract.MaxCursorBytes {
		return "", sessions.ErrEventLog
	}
	return encoded, nil
}

func writeCursorNumber(body *bytes.Buffer, value uint64) {
	var encoded [binary.MaxVarintLen64]byte
	count := binary.PutUvarint(encoded[:], value)
	body.Write(encoded[:count])
}

func writeCursorString(body *bytes.Buffer, value string) {
	writeCursorNumber(body, uint64(len(value)))
	body.WriteString(value)
}

func parseChatStreamCursor(key []byte, header http.Header, chat string) (string, error) {
	var values []string
	for key, items := range header {
		if strings.EqualFold(key, "Last-Event-ID") {
			if values != nil || len(items) != 1 {
				return "", sessions.ErrEventLog
			}
			values = items
		}
	}
	if values == nil {
		return "", nil
	}
	value := values[0]
	if value == "" || len(value) > contract.MaxCursorBytes {
		return "", sessions.ErrEventLog
	}
	body, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(key) != 32 || len(body) < 33 || body[0] != 1 {
		return "", sessions.ErrEventLog
	}
	payload, tag := body[:len(body)-16], body[len(body)-16:]
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if subtle.ConstantTimeCompare(tag, mac.Sum(nil)[:16]) != 1 {
		return "", sessions.ErrEventLog
	}
	subject := sha256.Sum256([]byte(chat))
	if subtle.ConstantTimeCompare(payload[1:17], subject[:16]) != 1 {
		return "", sessions.ErrEventLog
	}
	reader := bytes.NewReader(payload[17:])
	last, err := binary.ReadUvarint(reader)
	if err != nil {
		return "", sessions.ErrEventLog
	}
	high, err := binary.ReadUvarint(reader)
	if err != nil || last > uint64(^uint64(0)>>1) || high > uint64(^uint64(0)>>1) {
		return "", sessions.ErrEventLog
	}
	lastID, err := readCursorString(reader)
	if err != nil {
		return "", sessions.ErrEventLog
	}
	highID, err := readCursorString(reader)
	if err != nil || reader.Len() != 0 {
		return "", sessions.ErrEventLog
	}
	ledger, err := sessions.EncodeEventLogCursor(sessions.EventLogCursor{Version: 1, LastPosition: int64(last), LastEventID: lastID, HighWaterPosition: int64(high), HighWaterEventID: highID})
	if err != nil {
		return "", sessions.ErrEventLog
	}
	canonical, err := encodeChatStreamCursor(key, chat, ledger)
	if err != nil || canonical != value {
		return "", sessions.ErrEventLog
	}
	return ledger, nil
}

func readCursorString(reader *bytes.Reader) (string, error) {
	size, err := binary.ReadUvarint(reader)
	if err != nil || size > 128 || size > uint64(reader.Len()) {
		return "", sessions.ErrEventLog
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(reader, body); err != nil {
		return "", sessions.ErrEventLog
	}
	return string(body), nil
}

type preparedChatPage struct {
	events     []contract.Event
	nextLedger string
	lastCursor string
	hasMore    bool
	scanned    int
}

func prepareChatPage(key []byte, chat string, page sessions.CommittedEventPage) (preparedChatPage, error) {
	var prepared preparedChatPage
	if page.Validate() != nil || len(page.Events) > chatStreamPageLimit {
		return prepared, sessions.ErrEventLog
	}
	pageCursor, err := sessions.ParseEventLogCursor(page.NextCursor)
	if err != nil {
		return prepared, sessions.ErrEventLog
	}
	prepared.nextLedger, prepared.hasMore, prepared.scanned = page.NextCursor, page.HasMore, len(page.Events)
	for _, item := range page.Events {
		ledger, err := sessions.EncodeEventLogCursor(sessions.EventLogCursor{
			Version: 1, LastPosition: item.Position, LastEventID: item.Event.ID,
			HighWaterPosition: pageCursor.HighWaterPosition, HighWaterEventID: pageCursor.HighWaterEventID,
		})
		if err != nil {
			return preparedChatPage{}, sessions.ErrEventLog
		}
		cursor, err := encodeChatStreamCursor(key, chat, ledger)
		if err != nil {
			return preparedChatPage{}, err
		}
		if item.Event.SessionID != chat {
			continue
		}
		event, visible, err := projectChatEvent(chat, cursor, item)
		if err != nil {
			return preparedChatPage{}, err
		}
		if visible {
			prepared.events = append(prepared.events, event)
			prepared.lastCursor = cursor
		}
	}
	next, err := encodeChatStreamCursor(key, chat, page.NextCursor)
	if err != nil {
		return preparedChatPage{}, err
	}
	if pageCursor.LastPosition > 0 && prepared.lastCursor != next {
		checkpoint, err := newCommittedChatEvent(chat, next, pageCursor.LastPosition, contract.CheckpointEvent, contract.CheckpointData{NextCursor: next, HasMore: page.HasMore})
		if err != nil {
			return preparedChatPage{}, err
		}
		prepared.events = append(prepared.events, checkpoint)
		prepared.lastCursor = next
	}
	return prepared, nil
}

func newCommittedChatEvent(chat, cursor string, revision int64, kind contract.EventKind, data any) (contract.Event, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return contract.Event{}, contract.ErrContract
	}
	event := contract.Event{Version: 1, Cursor: cursor, Kind: kind, Durability: contract.Committed, Subject: chat, Revision: revision, Data: body}
	if event.Validate() != nil {
		return contract.Event{}, contract.ErrContract
	}
	return event, nil
}

func projectChatEvent(chat, cursor string, item sessions.CommittedEvent) (contract.Event, bool, error) {
	e := item.Event
	var kind contract.EventKind
	var data any
	switch e.Kind {
	case runtime.TaskStarted:
		kind, data = contract.LifecycleEvent, contract.LifecycleData{TaskID: e.TaskID, State: "started"}
	case runtime.TurnStarted:
		kind, data = contract.ModelChanged, contract.ModelChangedData{TaskID: e.TaskID, TurnID: e.TurnID, State: "started"}
	case runtime.TurnCompleted:
		kind, data = contract.ModelChanged, contract.ModelChangedData{TaskID: e.TaskID, TurnID: e.TurnID, State: "completed"}
	case runtime.ModelDelta:
		return contract.Event{}, false, nil
	case runtime.ToolStarted:
		kind, data = contract.ToolChanged, contract.ToolChangedData{TaskID: e.TaskID, TurnID: e.TurnID, ToolCallID: e.Data.ToolCallID, ToolName: e.Data.ToolName, State: "started"}
	case runtime.ToolCompleted:
		code := ""
		if e.Data.Code != "" {
			code = "tool_error"
		}
		kind, data = contract.ToolChanged, contract.ToolChangedData{TaskID: e.TaskID, TurnID: e.TurnID, ToolCallID: e.Data.ToolCallID, ToolName: e.Data.ToolName, State: "completed", Effect: string(e.Data.Effect), Code: code}
	case runtime.RouteSelected:
		kind, data = contract.RouteChanged, contract.RouteChangedData{TaskID: e.TaskID, RouteID: e.RouteID, State: "selected"}
	case runtime.WorkerStarted, runtime.WorkerHeartbeat, runtime.WorkerCompleted:
		states := map[runtime.Kind]string{runtime.WorkerStarted: "started", runtime.WorkerHeartbeat: "heartbeat", runtime.WorkerCompleted: "completed"}
		kind, data = contract.WorkerChanged, contract.WorkerChangedData{TaskID: e.TaskID, WorkerID: e.WorkerID, State: states[e.Kind]}
	case runtime.ErrorRecorded:
		kind, data = contract.ErrorChanged, contract.ErrorChangedData{TaskID: e.TaskID, Code: "runtime_error"}
	case runtime.TaskCompleted:
		kind, data = contract.TaskTerminal, contract.TaskTerminalData{TaskID: e.TaskID, State: "completed"}
	case runtime.TaskFailed:
		kind, data = contract.TaskTerminal, contract.TaskTerminalData{TaskID: e.TaskID, State: "failed", Code: "task_failed"}
	case runtime.TaskCanceled:
		kind, data = contract.TaskTerminal, contract.TaskTerminalData{TaskID: e.TaskID, State: "canceled", Code: "task_canceled"}
	case runtime.EvaluationRecorded:
		kind, data = contract.LifecycleEvent, contract.LifecycleData{TaskID: e.TaskID, State: "evaluated"}
	case runtime.SteeringApplied:
		kind, data = contract.LifecycleEvent, contract.LifecycleData{TaskID: e.TaskID, State: "steered"}
	case runtime.ContextCompacted:
		kind, data = contract.LifecycleEvent, contract.LifecycleData{TaskID: e.TaskID, State: "compacted"}
	default:
		return contract.Event{}, false, contract.ErrContract
	}
	projected, err := newCommittedChatEvent(chat, cursor, item.Position, kind, data)
	return projected, err == nil, err
}

func (h *Handler) readChatPage(ctx context.Context, after string) (page sessions.CommittedEventPage, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("events unavailable")
		}
	}()
	readCtx, cancel := context.WithTimeout(ctx, chatStreamReadTime)
	defer cancel()
	return h.reads.CommittedEvents(readCtx, sessions.EventLogOptions{After: after, Limit: chatStreamPageLimit})
}

func (h *Handler) serveChatEvents(w http.ResponseWriter, r *http.Request, chat string) {
	if r.Method != http.MethodGet || r.Body != http.NoBody || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.ForceQuery || r.URL.RawQuery != "" || !h.authenticated(r) {
		h.writeError(w, r, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.reads.CommittedEvents == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable")
		return
	}
	ledger, err := parseChatStreamCursor(h.cursorKey[:], r.Header, chat)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_event_cursor")
		return
	}
	select {
	case h.streamSlots <- struct{}{}:
		defer func() { <-h.streamSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		h.writeError(w, r, http.StatusServiceUnavailable, "stream_capacity")
		return
	}
	if !chatStreamWriter(w) {
		h.writeError(w, r, http.StatusInternalServerError, "streaming_unavailable")
		return
	}
	live, cancelLive := h.liveText.subscribe(chat)
	defer cancelLive()
	page, err := h.readChatPage(r.Context(), ledger)
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		h.chatStreamReadFailure(w, r, err)
		return
	}
	prepared, err := prepareChatPage(h.cursorKey[:], chat, page)
	if err != nil {
		h.chatStreamReadFailure(w, r, err)
		return
	}
	// After admission, transport failures can only end observation. They must
	// never escape into recovery middleware that might append a JSON response.
	defer func() { _ = recover() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	ctx, cancel := context.WithTimeout(r.Context(), chatStreamLife)
	defer cancel()
	if err := flushChatStream(controller); err != nil {
		return
	}
	streamed, scanned, bytesWritten := 0, 0, 0
	terminalTasks := map[string]bool{}
	for {
		scanned += prepared.scanned
		for _, event := range prepared.events {
			body, _ := json.Marshal(event)
			frameBytes := len(body) + len(event.Kind) + len(event.Cursor) + len("event: \nid: \ndata: \n\n")
			if streamed >= chatStreamMaxEvents || bytesWritten+frameBytes > chatStreamMaxBytes || writeChatSSE(ctx, controller, w, event, body) != nil {
				return
			}
			streamed++
			bytesWritten += frameBytes
			if event.Kind == contract.TaskTerminal {
				var terminal contract.TaskTerminalData
				if json.Unmarshal(event.Data, &terminal) == nil {
					terminalTasks[terminal.TaskID] = true
				}
			}
		}
		ledger = prepared.nextLedger
		if streamed >= chatStreamMaxEvents || scanned >= chatStreamMaxEvents || bytesWritten >= chatStreamMaxBytes {
			return
		}
		if prepared.hasMore {
			page, err = h.readChatPage(ctx, ledger)
		} else {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-live:
				if !ok {
					live = nil
				} else if delta, visible := projectLiveChatDelta(chat, terminalTasks, chunk); visible {
					body, marshalErr := json.Marshal(delta)
					frameBytes := len(body) + len(delta.Kind) + len("event: \ndata: \n\n")
					if delta.Validate() != nil || marshalErr != nil || streamed >= chatStreamMaxEvents || bytesWritten+frameBytes > chatStreamMaxBytes || writeChatSSE(ctx, controller, w, delta, body) != nil {
						return
					}
					streamed++
					bytesWritten += frameBytes
				}
			case <-time.After(chatStreamPoll):
			}
			page, err = h.readChatPage(ctx, ledger)
		}
		if ctx.Err() != nil || err != nil {
			return
		}
		prepared, err = prepareChatPage(h.cursorKey[:], chat, page)
		if err != nil {
			return
		}
	}
}

func projectLiveChatDelta(chat string, terminalTasks map[string]bool, chunk liveTextChunk) (contract.Event, bool) {
	if terminalTasks[chunk.task] || !browserStreamID.MatchString(chunk.task) {
		return contract.Event{}, false
	}
	delta := contract.Event{Version: 1, Kind: contract.ChatDelta, Durability: contract.Provisional, Subject: chat, Data: mustChatEventData(contract.ChatDeltaData{TaskID: chunk.task, Text: chunk.text})}
	return delta, delta.Validate() == nil
}

func mustChatEventData(value any) json.RawMessage {
	body, _ := json.Marshal(value)
	return body
}

func (h *Handler) chatStreamReadFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sessions.ErrEventTooLarge):
		h.writeError(w, r, http.StatusRequestEntityTooLarge, "event_page_too_large")
	case errors.Is(err, sessions.ErrEventLog):
		h.writeError(w, r, http.StatusConflict, "event_cursor_conflict")
	default:
		h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable")
	}
}

func chatStreamWriter(w http.ResponseWriter) bool {
	for range 16 {
		switch writer := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = writer.Unwrap()
		default:
			return false
		}
	}
	return false
}

func writeChatSSE(ctx context.Context, controller *http.ResponseController, w http.ResponseWriter, event contract.Event, body []byte) error {
	if ctx.Err() != nil || event.Validate() != nil {
		return context.Canceled
	}
	err := controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	frame := "event: " + string(event.Kind) + "\n"
	if event.Cursor != "" {
		frame += "id: " + event.Cursor + "\n"
	}
	frame += "data: " + string(body) + "\n\n"
	n, err := io.WriteString(w, frame)
	if err != nil || n != len(frame) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return err
	}
	if err = controller.Flush(); err != nil {
		return err
	}
	err = controller.SetWriteDeadline(time.Time{})
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func flushChatStream(controller *http.ResponseController) error {
	err := controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if err = controller.Flush(); err != nil {
		return err
	}
	err = controller.SetWriteDeadline(time.Time{})
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
