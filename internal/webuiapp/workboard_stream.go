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
	"net/url"
	"strings"
	"time"

	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

const (
	boardStreamPageLimit = 100
	boardStreamMaxEvents = 1000
	boardStreamMaxBytes  = 1 << 20
	boardStreamReadTime  = 5 * time.Second
	boardStreamPoll      = 250 * time.Millisecond
	boardStreamFilterAll = "all"
)

type boardStreamCursor struct {
	after  int64
	high   int64
	ledger string
}

func encodeBoardStreamCursor(key []byte, board, principal, filter string, cursor boardStreamCursor) (string, error) {
	if len(key) != 32 || !contract.ValidID(board) || !contract.ValidID(principal) || !validBoardStreamFilter(filter) ||
		cursor.after < 1 || cursor.high < cursor.after || len(cursor.ledger) > contract.MaxCursorBytes {
		return "", contract.ErrContract
	}
	var body bytes.Buffer
	body.WriteByte(1)
	for _, subject := range []string{board, principal, filter} {
		digest := sha256.Sum256([]byte(subject))
		body.Write(digest[:16])
	}
	writeCursorNumber(&body, uint64(cursor.after))
	writeCursorNumber(&body, uint64(cursor.high))
	writeCursorString(&body, cursor.ledger)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body.Bytes())
	body.Write(mac.Sum(nil)[:16])
	encoded := base64.RawURLEncoding.EncodeToString(body.Bytes())
	if len(encoded) > contract.MaxCursorBytes {
		return "", contract.ErrContract
	}
	return encoded, nil
}

func parseBoardStreamCursor(key []byte, header http.Header, board, principal, filter string) (boardStreamCursor, error) {
	var result boardStreamCursor
	var values []string
	for name, items := range header {
		if strings.EqualFold(name, "Last-Event-ID") {
			if values != nil || len(items) != 1 {
				return result, contract.ErrContract
			}
			values = items
		}
	}
	if values == nil {
		return result, nil
	}
	value := values[0]
	if value == "" || len(value) > contract.MaxCursorBytes || len(key) != 32 ||
		!contract.ValidID(board) || !contract.ValidID(principal) || !validBoardStreamFilter(filter) {
		return result, contract.ErrContract
	}
	body, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(body) < 66 || body[0] != 1 {
		return result, contract.ErrContract
	}
	payload, tag := body[:len(body)-16], body[len(body)-16:]
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	if subtle.ConstantTimeCompare(tag, mac.Sum(nil)[:16]) != 1 {
		return result, contract.ErrContract
	}
	for index, subject := range []string{board, principal, filter} {
		digest := sha256.Sum256([]byte(subject))
		start := 1 + index*16
		if subtle.ConstantTimeCompare(payload[start:start+16], digest[:16]) != 1 {
			return result, contract.ErrContract
		}
	}
	reader := bytes.NewReader(payload[49:])
	after, err := binary.ReadUvarint(reader)
	if err != nil || after > uint64(^uint64(0)>>1) {
		return result, contract.ErrContract
	}
	high, err := binary.ReadUvarint(reader)
	if err != nil || high > uint64(^uint64(0)>>1) {
		return result, contract.ErrContract
	}
	ledger, err := readBoardCursorString(reader)
	result = boardStreamCursor{after: int64(after), high: int64(high), ledger: ledger}
	if err != nil || reader.Len() != 0 || result.after < 1 || result.high < result.after {
		return boardStreamCursor{}, contract.ErrContract
	}
	canonical, err := encodeBoardStreamCursor(key, board, principal, filter, result)
	if err != nil || canonical != value {
		return boardStreamCursor{}, contract.ErrContract
	}
	return result, nil
}

func readBoardCursorString(reader *bytes.Reader) (string, error) {
	size, err := binary.ReadUvarint(reader)
	if err != nil || size > contract.MaxCursorBytes || size > uint64(reader.Len()) {
		return "", contract.ErrContract
	}
	body := make([]byte, int(size))
	if _, err = io.ReadFull(reader, body); err != nil {
		return "", contract.ErrContract
	}
	return string(body), nil
}

type preparedBoardPage struct {
	events     []contract.Event
	nextLedger string
	after      int64
	high       int64
	hasMore    bool
}

func prepareBoardPage(key []byte, board, principal, filter, pageStart string, resume boardStreamCursor, page contract.BoardEventPage) (preparedBoardPage, error) {
	prepared := preparedBoardPage{after: resume.after, high: page.HighWaterSequence, hasMore: page.HasMore, nextLedger: page.NextCursor}
	if page.Validate() != nil || page.BoardID != board || len(page.Items) > boardStreamPageLimit ||
		resume.high > 0 && (pageStart != "" && page.HighWaterSequence != resume.high || pageStart == "" && page.HighWaterSequence < resume.high) {
		return preparedBoardPage{}, contract.ErrContract
	}
	if page.HighWaterSequence == 0 && len(page.Items) != 0 || page.HighWaterSequence > 0 && len(page.Items) == 0 ||
		len(page.Items) > 0 && pageStart == "" && resume.after == 0 && page.Items[0].Sequence != 1 ||
		len(page.Items) > 0 && !page.HasMore && page.Items[len(page.Items)-1].Sequence != page.HighWaterSequence {
		return preparedBoardPage{}, contract.ErrContract
	}
	for index, item := range page.Items {
		if index > 0 && item.Sequence != page.Items[index-1].Sequence+1 {
			return preparedBoardPage{}, contract.ErrContract
		}
		if item.Sequence <= resume.after {
			continue
		}
		if item.Sequence != prepared.after+1 {
			return preparedBoardPage{}, contract.ErrContract
		}
		if !boardStreamEventVisible(filter, item) {
			prepared.after = item.Sequence
			continue
		}
		anchor := pageStart
		if index == len(page.Items)-1 {
			anchor = page.NextCursor
		}
		cursor, err := encodeBoardStreamCursor(key, board, principal, filter, boardStreamCursor{after: item.Sequence, high: page.HighWaterSequence, ledger: anchor})
		if err != nil {
			return preparedBoardPage{}, err
		}
		event, err := projectBoardEvent(board, cursor, item)
		if err != nil {
			return preparedBoardPage{}, err
		}
		prepared.events = append(prepared.events, event)
		prepared.after = item.Sequence
	}
	return prepared, nil
}

func boardStreamEventVisible(filter string, event contract.BoardEvent) bool {
	return filter == boardStreamFilterAll || event.CardID == strings.TrimPrefix(filter, "card:") || event.CardID == ""
}

func validBoardStreamFilter(filter string) bool {
	return filter == boardStreamFilterAll || strings.HasPrefix(filter, "card:") && contract.ValidID(strings.TrimPrefix(filter, "card:"))
}

func parseBoardStreamFilter(raw string) (string, error) {
	if raw == "" {
		return boardStreamFilterAll, nil
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) != 1 || len(values["card_id"]) != 1 {
		return "", contract.ErrContract
	}
	card := values["card_id"][0]
	if !contract.ValidID(card) || raw != "card_id="+card {
		return "", contract.ErrContract
	}
	return "card:" + card, nil
}

func projectBoardEvent(board, cursor string, event contract.BoardEvent) (contract.Event, error) {
	change := "card_changed"
	switch event.Kind {
	case contract.BoardCreate:
		change = "board_created"
	case contract.BoardRevise, contract.BoardArchive:
		change = "board_revised"
	case contract.DependencyAdd, contract.DependencyRemove:
		change = "dependency_changed"
	case contract.CardClaim, contract.ClaimHeartbeat, contract.ClaimRecover, contract.ClaimFail, contract.ClaimAttention:
		change = "claim_changed"
	case contract.CheckpointAppend, contract.CandidateSubmit, contract.CriteriaRevise:
		change = "evidence_changed"
	case contract.AcceptanceAccept, contract.AcceptanceReject:
		change = "acceptance_changed"
	}
	data, err := json.Marshal(contract.BoardChangedData{BoardID: board, CardID: event.CardID, Change: change})
	if err != nil {
		return contract.Event{}, contract.ErrContract
	}
	projected := contract.Event{Version: 1, Cursor: cursor, Kind: contract.BoardChanged, Durability: contract.Committed, Subject: board, Revision: event.Sequence, Data: data}
	if event.Validate() != nil || event.BoardID != board || projected.Validate() != nil {
		return contract.Event{}, contract.ErrContract
	}
	return projected, nil
}

func (h *Handler) readBoardEventPage(ctx context.Context, subject, board, after string, tailAfter int64) (page contract.BoardEventPage, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("workboard events unavailable")
		}
	}()
	readContext, cancel := context.WithTimeout(ctx, boardStreamReadTime)
	defer cancel()
	return h.workboards.Events(readContext, subject, board, contract.BoardEventOptions{After: after, Limit: boardStreamPageLimit, TailAfterSequence: tailAfter})
}

func (h *Handler) serveBoardEvents(writer http.ResponseWriter, request *http.Request, board string) {
	if request.Method != http.MethodGet || request.Body != http.NoBody || request.ContentLength != 0 ||
		len(request.TransferEncoding) != 0 || request.URL.ForceQuery ||
		!h.authenticated(request) || !h.sameOriginObservation(request) {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.workboards.Events == nil {
		h.writeError(writer, request, http.StatusServiceUnavailable, "workboard_events_unavailable")
		return
	}
	filter, err := parseBoardStreamFilter(request.URL.RawQuery)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_workboard_query")
		return
	}
	subject, ok := h.browserSubject(request)
	if !ok {
		h.writeError(writer, request, http.StatusUnauthorized, "unauthorized")
		return
	}
	resume, err := parseBoardStreamCursor(h.cursorKey[:], request.Header, board, subject, filter)
	if err != nil {
		h.writeError(writer, request, http.StatusBadRequest, "invalid_event_cursor")
		return
	}
	select {
	case h.boardStreamSlots <- struct{}{}:
		defer func() { <-h.boardStreamSlots }()
	default:
		writer.Header().Set("Retry-After", "1")
		h.writeError(writer, request, http.StatusServiceUnavailable, "stream_capacity")
		return
	}
	if !chatStreamWriter(writer) {
		h.writeError(writer, request, http.StatusInternalServerError, "streaming_unavailable")
		return
	}
	pageStart := resume.ledger
	initialTail := int64(0)
	if pageStart == "" {
		initialTail = resume.after
	}
	page, err := h.readBoardEventPage(request.Context(), subject, board, pageStart, initialTail)
	if request.Context().Err() != nil {
		return
	}
	if err != nil {
		h.workboardFailure(writer, request, err)
		return
	}
	prepared, err := prepareBoardPage(h.cursorKey[:], board, subject, filter, pageStart, resume, page)
	if err != nil {
		h.workboardFailure(writer, request, err)
		return
	}
	defer func() { _ = recover() }()
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(writer)
	streamContext, cancel := context.WithTimeout(request.Context(), h.boardStreamLife)
	defer cancel()
	if flushChatStream(controller) != nil {
		return
	}
	streamed, bytesWritten := 0, 0
	for {
		for _, event := range prepared.events {
			body, marshalErr := json.Marshal(event)
			frameBytes := len(body) + len(event.Kind) + len(event.Cursor) + len("event: \nid: \ndata: \n\n")
			if marshalErr != nil || streamed >= boardStreamMaxEvents || bytesWritten+frameBytes > boardStreamMaxBytes ||
				writeChatSSE(streamContext, controller, writer, event, body) != nil {
				return
			}
			streamed++
			bytesWritten += frameBytes
		}
		resume = boardStreamCursor{after: prepared.after, high: prepared.high, ledger: prepared.nextLedger}
		pageStart = prepared.nextLedger
		if streamed >= boardStreamMaxEvents || bytesWritten >= boardStreamMaxBytes {
			return
		}
		if !prepared.hasMore {
			resume.ledger, pageStart = "", ""
			select {
			case <-streamContext.Done():
				return
			case <-time.After(boardStreamPoll):
			}
		}
		tailAfter := int64(0)
		if pageStart == "" {
			tailAfter = resume.after
		}
		page, err = h.readBoardEventPage(streamContext, subject, board, pageStart, tailAfter)
		if streamContext.Err() != nil || err != nil {
			return
		}
		prepared, err = prepareBoardPage(h.cursorKey[:], board, subject, filter, pageStart, resume, page)
		if err != nil {
			return
		}
	}
}

func (h *Handler) sameOriginObservation(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return request.Header.Get("Sec-Fetch-Site") == "" || request.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	expected := scheme + "://" + request.Host
	return origin == expected && (request.Header.Get("Sec-Fetch-Site") == "" || request.Header.Get("Sec-Fetch-Site") == "same-origin") &&
		(len(h.origins) == 0 || h.origins[expected])
}
