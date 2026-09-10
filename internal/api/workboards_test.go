package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

const workboardKey = "workboard-operation-key-01"

func nativeWorkboardHandler(t *testing.T, configure func(*Services)) *Handler {
	t.Helper()
	services := services()
	configure(&services)
	handler, err := New(token, 1, services)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func nativeWorkboardRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == http.MethodGet && body == "" {
		request.Body, request.ContentLength = http.NoBody, 0
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func validNativeBoard() contract.Board {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	return contract.Board{Version: 1, ID: "board-a", Revision: 1, LayoutRevision: 1, EventSequence: 1, State: "active", Title: "Board", CardCount: 0, ActiveClaims: 0, CreatedAt: now, UpdatedAt: now}
}

func validNativeSnapshot() contract.BoardSnapshot {
	board := validNativeBoard()
	states := []string{"backlog", "ready", "in_progress", "blocked", "review", "done", "canceled"}
	titles := []string{"Backlog", "Ready", "In Progress", "Blocked", "Review", "Done", "Canceled"}
	columns := make([]contract.Column, len(states))
	for i := range states {
		columns[i] = contract.Column{Version: 1, ID: states[i], BoardID: board.ID, State: states[i], Title: titles[i], Rank: string(rune('a' + i))}
	}
	return contract.BoardSnapshot{Version: 1, Board: board, Columns: columns, Cards: []contract.Card{}, GraphRevision: 1, GraphDigest: strings.Repeat("a", 64)}
}

func validNativeReceipt(board string) contract.OperationReceipt {
	return contract.OperationReceipt{Version: 1, BoardID: board, OperationID: workboardKey, RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1, EventCount: 1, TransactionBytes: 128, BoardRevision: 1, Outcome: "committed", CreatedAt: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
}

func TestNativeWorkboardReadRoutesAreAuthenticatedAndClosed(t *testing.T) {
	var calls atomic.Int32
	handler := nativeWorkboardHandler(t, func(services *Services) {
		services.WorkboardList = func(_ context.Context, options contract.BoardListOptions) (contract.Page, error) {
			calls.Add(1)
			if options.Limit != 1 || options.State != "active" {
				t.Fatal("query was not bound", options)
			}
			return contract.Page{Version: 1, Items: []contract.Board{validNativeBoard()}}, nil
		}
		services.WorkboardRead = func(_ context.Context, id string, options contract.BoardSnapshotOptions) (contract.BoardSnapshot, error) {
			calls.Add(1)
			if id != "board-a" || options.Limit != 100 || options.ClaimState != "unclaimed" {
				t.Fatal("snapshot request was not bound", id, options)
			}
			return validNativeSnapshot(), nil
		}
		services.WorkboardEvents = func(_ context.Context, id string, options contract.BoardEventOptions) (contract.BoardEventPage, error) {
			calls.Add(1)
			if id != "board-a" || options.Limit != 100 {
				t.Fatal("event request was not bound", id, options)
			}
			return contract.BoardEventPage{Version: 1, BoardID: id, Items: []contract.BoardEvent{}, HighWaterSequence: 0}, nil
		}
	})

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/workboards?limit=1&state=active", nil))
	if unauthorized.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("unauthorized read dispatched", unauthorized.Code, calls.Load())
	}
	origin := nativeWorkboardRequest(http.MethodGet, "/v1/workboards?limit=1&state=active", "")
	origin.Header.Set("Origin", "https://example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, origin)
	if response.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatal("browser origin dispatched", response.Code, calls.Load())
	}

	for _, target := range []string{"/v1/workboards?limit=01", "/v1/workboards?limit=1&limit=1", "/v1/workboards?unknown=x", "/v1/workboards?"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, nativeWorkboardRequest(http.MethodGet, target, ""))
		if response.Code != http.StatusBadRequest || calls.Load() != 0 {
			t.Fatalf("invalid query dispatched: %s status=%d calls=%d", target, response.Code, calls.Load())
		}
	}

	for _, target := range []string{"/v1/workboards?limit=1&state=active", "/v1/workboards/board-a?claim_state=unclaimed", "/v1/workboards/board-a/events"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, nativeWorkboardRequest(http.MethodGet, target, ""))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", target, response.Code, response.Body.String())
		}
	}
	if calls.Load() != 3 {
		t.Fatal("unexpected calls", calls.Load())
	}
}

func TestNativeWorkboardMutationsBindPathActionAndIdempotency(t *testing.T) {
	var calls atomic.Int32
	handler := nativeWorkboardHandler(t, func(services *Services) {
		services.WorkboardMutate = func(_ context.Context, input contract.BoardRequest) (contract.OperationReceipt, error) {
			calls.Add(1)
			board := input.BoardID
			if board == "" {
				board = "board-a"
			}
			return validNativeReceipt(board), nil
		}
	})
	title := "Board"
	create, _ := json.Marshal(contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: workboardKey, Title: &title})
	revision := int64(1)
	archive, _ := json.Marshal(contract.BoardRequest{Version: 1, Action: contract.BoardArchive, IdempotencyKey: workboardKey, BoardID: "board-a", ExpectedBoardRevision: &revision})

	valid := []struct {
		path string
		body []byte
	}{{"/v1/workboards", create}, {"/v1/workboards/board-a/operations", archive}}
	for _, test := range valid {
		request := nativeWorkboardRequest(http.MethodPost, test.path, string(test.body))
		request.Header.Set("Idempotency-Key", workboardKey)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", test.path, response.Code, response.Body.String())
		}
	}

	bad := []struct {
		name, path, body string
		headers          []string
	}{
		{"missing", "/v1/workboards", string(create), nil},
		{"duplicate", "/v1/workboards", string(create), []string{workboardKey, workboardKey}},
		{"combined", "/v1/workboards", string(create), []string{workboardKey + "," + workboardKey}},
		{"mismatch", "/v1/workboards", string(create), []string{"another-operation-key-01"}},
		{"foreign-board", "/v1/workboards/board-b/operations", string(archive), []string{workboardKey}},
		{"create-on-operations", "/v1/workboards/board-a/operations", string(create), []string{workboardKey}},
		{"archive-on-create", "/v1/workboards", string(archive), []string{workboardKey}},
		{"unknown-field", "/v1/workboards", strings.TrimSuffix(string(create), "}") + `,"secret":"x"}`, []string{workboardKey}},
	}
	for _, test := range bad {
		t.Run(test.name, func(t *testing.T) {
			request := nativeWorkboardRequest(http.MethodPost, test.path, test.body)
			if test.headers != nil {
				request.Header["Idempotency-Key"] = test.headers
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
	if calls.Load() != 2 {
		t.Fatal("invalid request dispatched", calls.Load())
	}

	request := nativeWorkboardRequest(http.MethodPost, "/v1/workboards", string(create))
	request.Header.Del("Idempotency-Key")
	request.Header["idempotency-key"] = []string{workboardKey}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal("case-insensitive header rejected", response.Code, response.Body.String())
	}
	request = nativeWorkboardRequest(http.MethodPost, "/v1/workboards", string(create))
	request.Header["Idempotency-Key"] = []string{workboardKey}
	request.Header["idempotency-key"] = []string{workboardKey}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatal("case-split duplicate header accepted", response.Code)
	}
}

func TestNativeWorkboardMethodsAndBodiesAreStrict(t *testing.T) {
	var calls atomic.Int32
	handler := nativeWorkboardHandler(t, func(services *Services) {
		services.WorkboardMutate = func(context.Context, contract.BoardRequest) (contract.OperationReceipt, error) {
			calls.Add(1)
			return validNativeReceipt("board-a"), nil
		}
	})
	title := "Board"
	body, _ := json.Marshal(contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: workboardKey, Title: &title})
	for _, test := range []struct {
		name, method, path, media string
		mutate                    func(*http.Request)
		status                    int
	}{
		{name: "collection method", method: http.MethodPut, path: "/v1/workboards", status: http.StatusMethodNotAllowed},
		{name: "read method", method: http.MethodPost, path: "/v1/workboards/board-a", status: http.StatusMethodNotAllowed},
		{name: "event method", method: http.MethodPost, path: "/v1/workboards/board-a/events", status: http.StatusMethodNotAllowed},
		{name: "operation method", method: http.MethodGet, path: "/v1/workboards/board-a/operations", status: http.StatusMethodNotAllowed},
		{name: "media", method: http.MethodPost, path: "/v1/workboards", media: "text/plain", status: http.StatusBadRequest},
		{name: "media parameters", method: http.MethodPost, path: "/v1/workboards", media: "application/json; charset=utf-8", status: http.StatusBadRequest},
		{name: "unknown length", method: http.MethodPost, path: "/v1/workboards", mutate: func(r *http.Request) { r.ContentLength = -1 }, status: http.StatusBadRequest},
		{name: "transfer encoding", method: http.MethodPost, path: "/v1/workboards", mutate: func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestBody := ""
			if test.method == http.MethodPost {
				requestBody = string(body)
			}
			request := nativeWorkboardRequest(test.method, test.path, requestBody)
			request.Header.Set("Idempotency-Key", workboardKey)
			if test.media != "" {
				request.Header.Set("Content-Type", test.media)
			}
			if test.mutate != nil {
				test.mutate(request)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
	oversize := nativeWorkboardRequest(http.MethodPost, "/v1/workboards", strings.Repeat("x", contract.MaxRequestBytes+1))
	oversize.Header.Set("Idempotency-Key", workboardKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, oversize)
	if response.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatal("oversize request dispatched", response.Code, calls.Load())
	}
}

func TestNativeWorkboardOutputFailureAndCapacityAreSanitized(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	handler := nativeWorkboardHandler(t, func(services *Services) {
		services.WorkboardList = func(ctx context.Context, _ contract.BoardListOptions) (contract.Page, error) {
			started <- struct{}{}
			select {
			case <-release:
				return contract.Page{Version: 1}, nil
			case <-ctx.Done():
				return contract.Page{}, ctx.Err()
			}
		}
	})
	var group sync.WaitGroup
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, nativeWorkboardRequest(http.MethodGet, "/v1/workboards", ""))
		}()
	}
	for range 4 {
		<-started
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, nativeWorkboardRequest(http.MethodGet, "/v1/workboards", ""))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" || strings.Contains(response.Body.String(), "private") {
		t.Fatal(response.Code, response.Body.String())
	}
	close(release)
	group.Wait()

	handler = nativeWorkboardHandler(t, func(services *Services) {
		services.WorkboardList = func(ctx context.Context, _ contract.BoardListOptions) (contract.Page, error) {
			return contract.Page{}, ctx.Err()
		}
	})
	request := nativeWorkboardRequest(http.MethodGet, "/v1/workboards", "").WithContext(expiredContext())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), "workboard_timeout") {
		t.Fatal(response.Code, response.Body.String())
	}

	handler = nativeWorkboardHandler(t, func(services *Services) {
		services.WorkboardRead = func(context.Context, string, contract.BoardSnapshotOptions) (contract.BoardSnapshot, error) {
			return contract.BoardSnapshot{}, errors.New("private database path /secret")
		}
	})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, nativeWorkboardRequest(http.MethodGet, "/v1/workboards/board-a", ""))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private") || !json.Valid(response.Body.Bytes()) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func expiredContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	return ctx
}
