package webuiapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/browserauth"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func TestBoardStreamRestartRequiresFreshSnapshotForNewBrowserPrincipal(t *testing.T) {
	services := WorkboardServices{Events: func(context.Context, string, string, contract.BoardEventOptions) (contract.BoardEventPage, error) {
		return boardEventPage(), nil
	}}
	newHandler := func() *Handler {
		store, err := browserauth.New(browserauth.Options{SessionTTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := New(Options{BasePath: "/app", AllowedHosts: []string{"127.0.0.1:7788"}, Store: store,
			CursorKey: testStreamCursorKey, Workboards: services})
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
	before := newHandler()
	oldCookie, _ := authenticateBrowser(t, before)
	oldPrincipal, ok := before.store.Subject(oldCookie.Value)
	if !ok {
		t.Fatal("old principal unavailable")
	}
	cursor, err := encodeBoardStreamCursor(before.cursorKey[:], "board-a", oldPrincipal, boardStreamFilterAll,
		boardStreamCursor{after: 1, high: 2})
	if err != nil {
		t.Fatal(err)
	}
	after := newHandler()
	newCookie, _ := authenticateBrowser(t, after)
	request := authenticatedBoardStreamRequest(context.Background(), newCookie)
	request.Header.Set("Last-Event-ID", cursor)
	response := httptest.NewRecorder()
	after.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") == "text/event-stream" {
		t.Fatalf("cursor from expired process-local authority was not failed closed: %d %s", response.Code, response.Body.String())
	}
}

func boardEvent(sequence int64, action contract.BoardAction, card string) contract.BoardEvent {
	return contract.BoardEvent{Version: 1, ID: fmt.Sprintf("event-%06d", sequence), BoardID: "board-a", Sequence: sequence,
		OperationID: fmt.Sprintf("operation-%06d", sequence), Kind: action, ActorID: "operator", ActorType: "operator",
		CardID: card, CreatedAt: time.Date(2026, 9, 9, 20, 0, int(sequence), 0, time.UTC)}
}

func boardEventPage() contract.BoardEventPage {
	return contract.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: 2,
		Items: []contract.BoardEvent{boardEvent(1, contract.BoardCreate, ""), boardEvent(2, contract.CardCreate, "card-a")}}
}

func TestBoardStreamCursorBindsBoardFilterAndPrincipal(t *testing.T) {
	principal := strings.Repeat("a", 64)
	want := boardStreamCursor{after: 7, high: 11, ledger: "durable-page-cursor"}
	encoded, err := encodeBoardStreamCursor(testStreamCursorKey, "board-a", principal, boardStreamFilterAll, want)
	if err != nil || len(encoded) > contract.MaxCursorBytes {
		t.Fatal("encode", len(encoded), err)
	}
	if _, err = encodeBoardStreamCursor(testStreamCursorKey, "board-a", principal, "unknown-filter", want); err == nil {
		t.Fatal("noncanonical filter encoded")
	}
	got, err := parseBoardStreamCursor(testStreamCursorKey, http.Header{"Last-Event-ID": {encoded}}, "board-a", principal, boardStreamFilterAll)
	if err != nil || got != want {
		t.Fatal("round trip", got, err)
	}
	for name, binding := range map[string][3]string{
		"board":     {"board-b", principal, boardStreamFilterAll},
		"principal": {"board-a", strings.Repeat("b", 64), boardStreamFilterAll},
		"filter":    {"board-a", principal, "card:card-a"},
	} {
		if _, err = parseBoardStreamCursor(testStreamCursorKey, http.Header{"Last-Event-ID": {encoded}}, binding[0], binding[1], binding[2]); err == nil {
			t.Fatal("accepted cross-bound cursor", name)
		}
	}
	body, _ := base64.RawURLEncoding.DecodeString(encoded)
	body[len(body)-1] ^= 1
	if _, err = parseBoardStreamCursor(testStreamCursorKey, http.Header{"Last-Event-ID": {base64.RawURLEncoding.EncodeToString(body)}}, "board-a", principal, boardStreamFilterAll); err == nil {
		t.Fatal("accepted forged cursor")
	}
	headers := http.Header{"Last-Event-ID": {encoded, encoded}}
	if _, err = parseBoardStreamCursor(testStreamCursorKey, headers, "board-a", principal, boardStreamFilterAll); err == nil {
		t.Fatal("accepted duplicate cursor header")
	}
}

func TestBoardStreamReconnectHasNoGapOrDuplicate(t *testing.T) {
	principal := strings.Repeat("a", 64)
	firstPage := contract.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: 3,
		Items:      []contract.BoardEvent{boardEvent(1, contract.BoardCreate, ""), boardEvent(2, contract.CardCreate, "card-a")},
		NextCursor: "durable-next", HasMore: true}
	first, err := prepareBoardPage(testStreamCursorKey, "board-a", principal, boardStreamFilterAll, "", boardStreamCursor{}, firstPage)
	if err != nil || len(first.events) != 2 {
		t.Fatal("first page", first, err)
	}
	resumeOne, err := parseBoardStreamCursor(testStreamCursorKey, http.Header{"Last-Event-ID": {first.events[0].Cursor}}, "board-a", principal, boardStreamFilterAll)
	if err != nil || resumeOne.after != 1 || resumeOne.ledger != "" {
		t.Fatal("mid-page cursor", resumeOne, err)
	}
	replayed, err := prepareBoardPage(testStreamCursorKey, "board-a", principal, boardStreamFilterAll, "", resumeOne, firstPage)
	if err != nil || len(replayed.events) != 1 || replayed.events[0].Revision != 2 {
		t.Fatal("mid-page replay", replayed, err)
	}
	resumeTwo, err := parseBoardStreamCursor(testStreamCursorKey, http.Header{"Last-Event-ID": {first.events[1].Cursor}}, "board-a", principal, boardStreamFilterAll)
	if err != nil || resumeTwo.after != 2 || resumeTwo.ledger != "durable-next" {
		t.Fatal("page-boundary cursor", resumeTwo, err)
	}
	lastPage := contract.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: 3,
		Items: []contract.BoardEvent{boardEvent(3, contract.CardRevise, "card-a")}}
	last, err := prepareBoardPage(testStreamCursorKey, "board-a", principal, boardStreamFilterAll, resumeTwo.ledger, resumeTwo, lastPage)
	if err != nil || len(last.events) != 1 || last.events[0].Revision != 3 {
		t.Fatal("last page", last, err)
	}
	resumeThree, err := parseBoardStreamCursor(testStreamCursorKey, http.Header{"Last-Event-ID": {last.events[0].Cursor}}, "board-a", principal, boardStreamFilterAll)
	if err != nil || resumeThree.after != 3 || resumeThree.ledger != "" {
		t.Fatal("head cursor", resumeThree, err)
	}
	newHead := contract.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: 4,
		Items: []contract.BoardEvent{boardEvent(1, contract.BoardCreate, ""), boardEvent(2, contract.CardCreate, "card-a"), boardEvent(3, contract.CardRevise, "card-a"), boardEvent(4, contract.CardReorder, "card-a")}}
	fresh, err := prepareBoardPage(testStreamCursorKey, "board-a", principal, boardStreamFilterAll, "", resumeThree, newHead)
	if err != nil || len(fresh.events) != 1 || fresh.events[0].Revision != 4 {
		t.Fatal("fresh head reconciliation", fresh, err)
	}
}

func TestBoardStreamFilterIsCanonicalAndCursorBound(t *testing.T) {
	filter, err := parseBoardStreamFilter("card_id=card-a")
	if err != nil || filter != "card:card-a" {
		t.Fatal("valid filter rejected", filter, err)
	}
	for _, raw := range []string{"card_id=card-a&card_id=card-a", "card_id=card%2Da", "unknown=card-a", "card_id="} {
		if _, err = parseBoardStreamFilter(raw); err == nil {
			t.Fatal("noncanonical filter accepted", raw)
		}
	}
	page := contract.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: 3, Items: []contract.BoardEvent{
		boardEvent(1, contract.BoardCreate, ""), boardEvent(2, contract.CardCreate, "card-a"), boardEvent(3, contract.CardCreate, "card-b"),
	}}
	principal := strings.Repeat("a", 64)
	prepared, err := prepareBoardPage(testStreamCursorKey, "board-a", principal, filter, "", boardStreamCursor{}, page)
	if err != nil || len(prepared.events) != 2 || prepared.events[0].Revision != 1 || prepared.events[1].Revision != 2 || prepared.after != 3 {
		t.Fatal("filter projection", prepared, err)
	}
	if _, err = parseBoardStreamCursor(testStreamCursorKey, http.Header{"Last-Event-ID": {prepared.events[1].Cursor}}, "board-a", principal, boardStreamFilterAll); err == nil {
		t.Fatal("filtered cursor was accepted by unfiltered stream")
	}
}

func authenticatedBoardStreamRequest(ctx context.Context, cookie *http.Cookie) *http.Request {
	request := browserWorkboardGET("/app/api/v1/workboards/board-a/events").WithContext(ctx)
	request.AddCookie(cookie)
	return request
}

func TestBoardStreamAuthenticatesAndValidatesOriginBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{Events: func(_ context.Context, subject, board string, options contract.BoardEventOptions) (contract.BoardEventPage, error) {
		calls.Add(1)
		if len(subject) != 64 || board != "board-a" || options.After != "" || options.Limit != boardStreamPageLimit {
			t.Fatal("incorrect service binding", len(subject), board, options)
		}
		return boardEventPage(), nil
	}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, browserWorkboardGET("/app/api/v1/workboards/board-a/events"))
	if response.Code != http.StatusUnauthorized || calls.Load() != 0 || response.Header().Get("Content-Type") == "text/event-stream" {
		t.Fatal("unauthorized stream dispatched", response.Code, calls.Load())
	}
	cookie, _ := authenticateBrowser(t, handler)
	foreign := authenticatedBoardStreamRequest(context.Background(), cookie)
	foreign.Header.Set("Origin", "http://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, foreign)
	if response.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("foreign origin dispatched", response.Code, calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	responseStream := &cancelFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, limit: 2}
	handler.ServeHTTP(responseStream, authenticatedBoardStreamRequest(ctx, cookie))
	if responseStream.Code != http.StatusOK || responseStream.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(responseStream.Body.String(), "event: board.changed") || strings.Contains(responseStream.Body.String(), "operator") || calls.Load() != 1 || len(handler.boardStreamSlots) != 0 {
		t.Fatal("authenticated stream failed", responseStream.Code, calls.Load(), responseStream.Body.String())
	}
}

func TestBoardStreamPrevalidatesBeforeHeadersAndRejectsCursor(t *testing.T) {
	var calls atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{Events: func(context.Context, string, string, contract.BoardEventOptions) (contract.BoardEventPage, error) {
		calls.Add(1)
		return contract.BoardEventPage{Version: 1, BoardID: "wrong", HighWaterSequence: 0}, nil
	}})
	cookie, _ := authenticateBrowser(t, handler)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedBoardStreamRequest(context.Background(), cookie))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Content-Type") == "text/event-stream" || calls.Load() != 1 {
		t.Fatal("invalid page crossed header boundary", response.Code, response.Header(), response.Body.String())
	}
	request := authenticatedBoardStreamRequest(context.Background(), cookie)
	request.Header.Set("Last-Event-ID", "forged")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatal("invalid cursor dispatched", response.Code, calls.Load())
	}
	request = authenticatedBoardStreamRequest(context.Background(), cookie)
	request.URL.RawQuery = "card_id=card%2Da"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatal("invalid filter dispatched", response.Code, calls.Load())
	}
}

func TestBoardStreamHasIndependentCapacityAndReleasesOnDisconnect(t *testing.T) {
	handler := browserWorkboardHandler(t, WorkboardServices{Events: func(context.Context, string, string, contract.BoardEventOptions) (contract.BoardEventPage, error) {
		return boardEventPage(), nil
	}})
	cookie, _ := authenticateBrowser(t, handler)
	for index := 0; index < cap(handler.slots); index++ {
		handler.slots <- struct{}{}
	}
	for index := 0; index < cap(handler.streamSlots); index++ {
		handler.streamSlots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream := &cancelFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, limit: 2}
	handler.ServeHTTP(stream, authenticatedBoardStreamRequest(ctx, cookie))
	if stream.Code != http.StatusOK || len(handler.boardStreamSlots) != 0 {
		t.Fatal("board stream did not use independent capacity", stream.Code, len(handler.boardStreamSlots))
	}
	for len(handler.slots) > 0 {
		<-handler.slots
	}
	for len(handler.streamSlots) > 0 {
		<-handler.streamSlots
	}
	for index := 0; index < cap(handler.boardStreamSlots); index++ {
		handler.boardStreamSlots <- struct{}{}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedBoardStreamRequest(context.Background(), cookie))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatal("capacity did not fail closed", response.Code, response.Header())
	}
	for len(handler.boardStreamSlots) > 0 {
		<-handler.boardStreamSlots
	}
}

func TestBoardStreamLifetimeIsBoundedAndNeverMutates(t *testing.T) {
	var reads, mutations atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{
		Events: func(ctx context.Context, _ string, _ string, _ contract.BoardEventOptions) (contract.BoardEventPage, error) {
			reads.Add(1)
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > boardStreamReadTime+time.Second {
				t.Fatal("event read was not bounded")
			}
			return contract.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: 0, Items: []contract.BoardEvent{}}, nil
		},
		Mutate: func(context.Context, string, contract.BoardRequest) (contract.OperationReceipt, error) {
			mutations.Add(1)
			return contract.OperationReceipt{}, nil
		},
	})
	handler.boardStreamLife = 10 * time.Millisecond
	cookie, _ := authenticateBrowser(t, handler)
	started := time.Now()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedBoardStreamRequest(context.Background(), cookie))
	if response.Code != http.StatusOK || time.Since(started) > time.Second || reads.Load() != 1 || mutations.Load() != 0 || len(handler.boardStreamSlots) != 0 {
		t.Fatal("bounded observation failed", response.Code, time.Since(started), reads.Load(), mutations.Load(), len(handler.boardStreamSlots))
	}
}

func TestBoardStreamCapsPagesEventsAndBytes(t *testing.T) {
	var reads atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{Events: func(_ context.Context, _ string, _ string, options contract.BoardEventOptions) (contract.BoardEventPage, error) {
		if options.Limit != 100 {
			t.Fatal("unbounded page request", options.Limit)
		}
		page := int(reads.Add(1)) - 1
		start := int64(page*100 + 1)
		items := make([]contract.BoardEvent, 100)
		for index := range items {
			items[index] = boardEvent(start+int64(index), contract.CardRevise, "card-a")
		}
		result := contract.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: 1000, Items: items}
		if page < 9 {
			result.HasMore = true
			result.NextCursor = fmt.Sprintf("durable-page-%d", page+1)
		}
		return result, nil
	}})
	cookie, _ := authenticateBrowser(t, handler)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedBoardStreamRequest(context.Background(), cookie))
	body := response.Body.String()
	if response.Code != http.StatusOK || reads.Load() != 10 || strings.Count(body, "event: board.changed") != boardStreamMaxEvents || len(body) > boardStreamMaxBytes {
		t.Fatal("stream bounds failed", response.Code, reads.Load(), strings.Count(body, "event: board.changed"), len(body))
	}
}

func TestBoardStreamPopulatedBoardPollsFromDurableTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reads, fullReads, tailReads atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{Events: func(_ context.Context, _ string, _ string, options contract.BoardEventOptions) (contract.BoardEventPage, error) {
		reads.Add(1)
		page := contract.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: 250}
		switch {
		case options.After == "" && options.TailAfterSequence == 0:
			fullReads.Add(1)
			page.Items = boardEvents(1, 100)
			page.HasMore, page.NextCursor = true, "page-100"
		case options.After == "page-100" && options.TailAfterSequence == 0:
			page.Items = boardEvents(101, 200)
			page.HasMore, page.NextCursor = true, "page-200"
		case options.After == "page-200" && options.TailAfterSequence == 0:
			page.Items = boardEvents(201, 250)
		case options.After == "" && options.TailAfterSequence == 250:
			tailReads.Add(1)
			page.Items = boardEvents(250, 250)
			cancel()
		default:
			t.Fatalf("unexpected event read options: %+v", options)
		}
		return page, nil
	}})
	handler.boardStreamLife = time.Second
	cookie, _ := authenticateBrowser(t, handler)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedBoardStreamRequest(ctx, cookie))
	if response.Code != http.StatusOK || reads.Load() != 4 || fullReads.Load() != 1 || tailReads.Load() != 1 ||
		strings.Count(response.Body.String(), "event: board.changed") != 250 {
		t.Fatalf("tail polling failed: code=%d reads=%d full=%d tail=%d events=%d", response.Code, reads.Load(), fullReads.Load(), tailReads.Load(), strings.Count(response.Body.String(), "event: board.changed"))
	}
}

func TestBoardStreamReconnectStartsAtDurableTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reads atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{Events: func(_ context.Context, _ string, _ string, options contract.BoardEventOptions) (contract.BoardEventPage, error) {
		read := reads.Add(1)
		if options.After != "" || options.TailAfterSequence != int64(249+read) {
			t.Fatalf("reconnect did not tail follow: read=%d options=%+v", read, options)
		}
		sequence := int64(250 + read)
		if read == 2 {
			cancel()
		}
		return contract.BoardEventPage{Version: 1, BoardID: "board-a", HighWaterSequence: sequence, Items: boardEvents(sequence, sequence)}, nil
	}})
	handler.boardStreamLife = time.Second
	cookie, _ := authenticateBrowser(t, handler)
	principal, ok := handler.store.Subject(cookie.Value)
	if !ok {
		t.Fatal("browser subject unavailable")
	}
	resume, err := encodeBoardStreamCursor(handler.cursorKey[:], "board-a", principal, boardStreamFilterAll, boardStreamCursor{after: 250, high: 250})
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedBoardStreamRequest(ctx, cookie)
	request.Header.Set("Last-Event-ID", resume)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reads.Load() != 2 || strings.Count(response.Body.String(), "event: board.changed") != 1 ||
		!strings.Contains(response.Body.String(), `"revision":251`) || strings.Contains(response.Body.String(), `"revision":250`) {
		t.Fatalf("reconnect tail failed: code=%d reads=%d body=%s", response.Code, reads.Load(), response.Body.String())
	}
}

func boardEvents(first, last int64) []contract.BoardEvent {
	items := make([]contract.BoardEvent, 0, last-first+1)
	for sequence := first; sequence <= last; sequence++ {
		action := contract.CardRevise
		card := "card-a"
		if sequence == 1 {
			action, card = contract.BoardCreate, ""
		}
		items = append(items, boardEvent(sequence, action, card))
	}
	return items
}

func TestProjectedBoardStreamEventIsMinimalAndCommitted(t *testing.T) {
	event := boardEvent(2, contract.CardMove, "card-a")
	projected, err := projectBoardEvent("board-a", "opaque-cursor", event)
	if err != nil || projected.Validate() != nil || projected.Kind != contract.BoardChanged || projected.Durability != contract.Committed {
		t.Fatal("invalid projection", projected, err)
	}
	body, _ := json.Marshal(projected)
	for _, secret := range []string{"operator", event.OperationID, event.ID, event.CreatedAt.Format(time.RFC3339)} {
		if strings.Contains(string(body), secret) {
			t.Fatal("durable metadata escaped browser projection", secret)
		}
	}
}

func TestBoardStreamProjectsLifecycleChangeClasses(t *testing.T) {
	for _, test := range []struct {
		action contract.BoardAction
		change string
	}{
		{contract.BoardCreate, "board_created"}, {contract.BoardArchive, "board_revised"},
		{contract.CardMove, "card_changed"}, {contract.DependencyAdd, "dependency_changed"},
		{contract.CardClaim, "claim_changed"}, {contract.ClaimAttention, "claim_changed"}, {contract.CheckpointAppend, "evidence_changed"},
		{contract.AcceptanceAccept, "acceptance_changed"},
	} {
		card := "card-a"
		if test.action == contract.BoardCreate || test.action == contract.BoardArchive {
			card = ""
		}
		projected, err := projectBoardEvent("board-a", "opaque-cursor", boardEvent(1, test.action, card))
		var data contract.BoardChangedData
		if err != nil || json.Unmarshal(projected.Data, &data) != nil || data.Change != test.change {
			t.Fatal("incorrect lifecycle projection", test.action, data, err)
		}
	}
}
