package webuiapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/browserauth"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

const browserWorkboardKey = "browser-workboard-key-01"

func browserWorkboardHandler(t *testing.T, services WorkboardServices) *Handler {
	t.Helper()
	store, err := browserauth.New(browserauth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{BasePath: "/app", AllowedHosts: []string{"127.0.0.1:7788"}, Store: store, Workboards: services})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func browserWorkboardGET(target string) *http.Request {
	request := browserRequest(http.MethodGet, target, "")
	request.Body, request.ContentLength = http.NoBody, 0
	return request
}

func validBrowserBoard() contract.Board {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	return contract.Board{Version: 1, ID: "board-a", Revision: 1, LayoutRevision: 1, EventSequence: 1, State: "active", Title: "Board", CardCount: 0, CreatedAt: now, UpdatedAt: now}
}

func validBrowserSnapshot() contract.BoardSnapshot {
	board := validBrowserBoard()
	states := []string{"backlog", "ready", "in_progress", "blocked", "review", "done", "canceled"}
	titles := []string{"Backlog", "Ready", "In Progress", "Blocked", "Review", "Done", "Canceled"}
	columns := make([]contract.Column, len(states))
	for i := range states {
		columns[i] = contract.Column{Version: 1, ID: states[i], BoardID: board.ID, State: states[i], Title: titles[i], Rank: string(rune('a' + i))}
	}
	return contract.BoardSnapshot{Version: 1, Board: board, Columns: columns, Cards: []contract.Card{}, GraphRevision: 1, GraphDigest: strings.Repeat("a", 64)}
}

func validBrowserReceipt(board string) contract.OperationReceipt {
	return contract.OperationReceipt{Version: 1, BoardID: board, OperationID: browserWorkboardKey, RequestDigest: strings.Repeat("a", 64), ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1, EventCount: 1, TransactionBytes: 128, BoardRevision: 1, Outcome: "committed", CreatedAt: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
}

func TestBrowserWorkboardReadsRequireSessionAndUseClosedQueries(t *testing.T) {
	var calls atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{
		List: func(_ context.Context, subject string, options contract.BoardListOptions) (contract.Page, error) {
			calls.Add(1)
			if len(subject) != 64 || options.Limit != 1 || options.State != "active" {
				t.Fatal("list binding", len(subject), options)
			}
			return contract.Page{Version: 1, Items: []contract.Board{validBrowserBoard()}}, nil
		},
		Read: func(_ context.Context, subject, board string, options contract.BoardSnapshotOptions) (contract.BoardSnapshot, error) {
			calls.Add(1)
			if len(subject) != 64 || board != "board-a" || options.Limit != 100 || options.OwnerID != "unassigned" {
				t.Fatal("read binding", len(subject), board, options)
			}
			return validBrowserSnapshot(), nil
		},
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, browserWorkboardGET("/app/api/v1/workboards?limit=1&state=active"))
	if response.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("unauthorized workboard read dispatched", response.Code, calls.Load())
	}
	cookie, _ := authenticateBrowser(t, handler)
	for _, target := range []string{"/app/api/v1/workboards?limit=01", "/app/api/v1/workboards?limit=1&limit=1", "/app/api/v1/workboards?unknown=x", "/app/api/v1/workboards?"} {
		request := browserWorkboardGET(target)
		request.AddCookie(cookie)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || calls.Load() != 0 {
			t.Fatalf("invalid query dispatched: %s status=%d calls=%d", target, response.Code, calls.Load())
		}
	}
	for _, target := range []string{"/app/api/v1/workboards?limit=1&state=active", "/app/api/v1/workboards/board-a?owner_id=unassigned"} {
		request := browserWorkboardGET(target)
		request.AddCookie(cookie)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", target, response.Code, response.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected workboard calls", calls.Load())
	}
	request := browserWorkboardGET("/app/api/v1/workboards/board-a/events")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Content-Type") != "application/json" {
		t.Fatal("unconfigured SSE route did not fail before stream headers", response.Code, response.Header(), response.Body.String())
	}
}

func TestBrowserLifecycleHistoryRoutesBindSessionAndClosedPath(t *testing.T) {
	var calls atomic.Int32
	attempt := validBrowserAttempt()
	handler := browserWorkboardHandler(t, WorkboardServices{
		AttemptHistory: func(_ context.Context, subject, board, card string, options contract.AttemptHistoryOptions) (contract.AttemptHistoryPage, error) {
			calls.Add(1)
			if len(subject) != 64 || board != "board-a" || card != "card-a" || options.Limit != 1 {
				t.Fatal("history binding", subject, board, card, options)
			}
			return contract.AttemptHistoryPage{Version: 1, BoardID: board, CardID: card, HighWaterOrdinal: 1,
				Items: []contract.AttemptHistoryRecord{{Version: 1, ID: attempt.ID, BoardID: board, CardID: card, Ordinal: 1,
					Revision: 1, State: "running", WorkerID: "worker-a", CriteriaRevision: 1, StartedAt: attempt.StartedAt}}}, nil
		},
		AttemptDetail: func(_ context.Context, subject, board, card, id string, options contract.AttemptDetailOptions) (contract.AttemptDetailPage, error) {
			calls.Add(1)
			if len(subject) != 64 || board != "board-a" || card != "card-a" || id != "attempt-a" || options.Limit != 25 {
				t.Fatal("detail binding", subject, board, card, id, options)
			}
			return contract.AttemptDetailPage{Version: 1, Attempt: attempt, Checkpoints: []contract.WorkCheckpoint{}}, nil
		},
	})
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, browserWorkboardGET("/app/api/v1/workboards/board-a/cards/card-a/attempts?limit=1"))
	if unauthorized.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("unauthorized history dispatched", unauthorized.Code, calls.Load())
	}
	cookie, _ := authenticateBrowser(t, handler)
	for _, target := range []string{"/app/api/v1/workboards/board-a/cards/card-a/attempts?limit=1",
		"/app/api/v1/workboards/board-a/cards/card-a/attempts/attempt-a"} {
		request := browserWorkboardGET(target)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", target, response.Code, response.Body.String())
		}
	}
	request := browserWorkboardGET("/app/api/v1/workboards/board-a/cards/card-a/attempts?limit=01")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || calls.Load() != 2 {
		t.Fatal("invalid history query dispatched", response.Code, calls.Load())
	}
}

func validBrowserAttempt() contract.Attempt {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	criteria := []contract.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "Tests pass.", Required: true}}
	return contract.Attempt{Version: 1, ID: "attempt-a", BoardID: "board-a", CardID: "card-a", Ordinal: 1, Revision: 1,
		State: "running", WorkerID: "worker-a", CriteriaRevision: 1, CriteriaDigest: contract.AcceptanceCriteriaDigest(criteria), PolicyDigest: strings.Repeat("b", 64),
		Budget: contract.WorkBudget{AttemptLimit: 3}, Criteria: criteria, TaskIDs: []string{}, SessionIDs: []string{},
		Claim: &contract.Claim{Version: 1, ID: "claim-a", BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", Revision: 1,
			State: "active", OwnerID: "worker-a", OwnerType: "worker", ExpiresAt: now.Add(time.Minute), LastHeartbeat: now},
		Evidence: []contract.EvidenceRecord{}, StartedAt: now}
}

func TestBrowserWorkboardMutationsRequireCSRFAndBindPath(t *testing.T) {
	var calls atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{Mutate: func(_ context.Context, subject string, input contract.BoardRequest) (contract.OperationReceipt, error) {
		calls.Add(1)
		if len(subject) != 64 {
			t.Fatal("missing browser subject")
		}
		board := input.BoardID
		if board == "" {
			board = "board-a"
		}
		return validBrowserReceipt(board), nil
	}})
	title := "Board"
	create, _ := json.Marshal(contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: browserWorkboardKey, Title: &title})
	revision := int64(1)
	archive, _ := json.Marshal(contract.BoardRequest{Version: 1, Action: contract.BoardArchive, IdempotencyKey: browserWorkboardKey, BoardID: "board-a", ExpectedBoardRevision: &revision})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, browserRequest(http.MethodPost, "/app/api/v1/workboards", string(create)))
	if response.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("unauthorized mutation dispatched", response.Code, calls.Load())
	}
	cookie, csrf := authenticateBrowser(t, handler)
	request := browserRequest(http.MethodPost, "/app/api/v1/workboards", string(create))
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatal("mutation without CSRF dispatched", response.Code, calls.Load())
	}
	wrongOrigin := browserRequest(http.MethodPost, "/app/api/v1/workboards", string(create))
	wrongOrigin.AddCookie(cookie)
	wrongOrigin.Header.Set("X-Darwin-CSRF", csrf)
	wrongOrigin.Header.Set("Origin", "http://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, wrongOrigin)
	if response.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatal("foreign origin dispatched", response.Code, calls.Load())
	}

	foreign := browserRequest(http.MethodPost, "/app/api/v1/workboards/board-b/operations", string(archive))
	foreign.AddCookie(cookie)
	foreign.Header.Set("X-Darwin-CSRF", csrf)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, foreign)
	if response.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatal("foreign path body dispatched", response.Code, calls.Load())
	}

	for _, test := range []struct {
		path string
		body []byte
	}{{"/app/api/v1/workboards", create}, {"/app/api/v1/workboards/board-a/operations", archive}} {
		request = browserRequest(http.MethodPost, test.path, string(test.body))
		request.AddCookie(cookie)
		request.Header.Set("X-Darwin-CSRF", csrf)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", test.path, response.Code, response.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected mutation calls", calls.Load())
	}

	for _, invalid := range []struct{ name, body, media string }{
		{"duplicate", `{"version":1,"version":1,"action":"board.create","idempotency_key":"browser-workboard-key-01","title":"Board"}`, "application/json"},
		{"unknown", `{"version":1,"action":"board.create","idempotency_key":"browser-workboard-key-01","title":"Board","secret":"x"}`, "application/json"},
		{"media", string(create), "text/plain"},
		{"oversize", strings.Repeat("x", contract.MaxRequestBytes+1), "application/json"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			request := browserRequest(http.MethodPost, "/app/api/v1/workboards", invalid.body)
			request.AddCookie(cookie)
			request.Header.Set("X-Darwin-CSRF", csrf)
			request.Header.Set("Content-Type", invalid.media)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
	if calls.Load() != 2 {
		t.Fatal("invalid browser mutation dispatched", calls.Load())
	}
}

func TestBrowserDependencyMutationPreservesClosedFences(t *testing.T) {
	var got contract.BoardRequest
	handler := browserWorkboardHandler(t, WorkboardServices{Mutate: func(_ context.Context, _ string, input contract.BoardRequest) (contract.OperationReceipt, error) {
		got = input
		receipt := validBrowserReceipt(input.BoardID)
		next := *input.ExpectedCardRevision + 1
		receipt.CardID, receipt.CardRevision, receipt.BoardRevision = input.CardID, &next, 7
		return receipt, nil
	}})
	cookie, csrf := authenticateBrowser(t, handler)
	request := browserRequest(http.MethodPost, "/app/api/v1/workboards/board-a/operations",
		`{"version":1,"action":"dependency.add","idempotency_key":"browser-workboard-key-01","board_id":"board-a","card_id":"card-a","dependency_id":"card-b","expected_card_revision":3,"expected_graph_revision":8}`)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || got.Action != contract.DependencyAdd || got.BoardID != "board-a" || got.CardID != "card-a" || got.DependencyID != "card-b" ||
		got.ExpectedCardRevision == nil || *got.ExpectedCardRevision != 3 || got.ExpectedGraphRevision == nil || *got.ExpectedGraphRevision != 8 {
		t.Fatalf("status=%d request=%+v body=%s", response.Code, got, response.Body.String())
	}
}

func TestBrowserCardControlMutationPreservesClosedFence(t *testing.T) {
	var got contract.BoardRequest
	handler := browserWorkboardHandler(t, WorkboardServices{Mutate: func(_ context.Context, _ string, input contract.BoardRequest) (contract.OperationReceipt, error) {
		got = input
		receipt := validBrowserReceipt(input.BoardID)
		next := *input.ExpectedCardRevision + 1
		receipt.CardID, receipt.CardRevision, receipt.BoardRevision = input.CardID, &next, 9
		return receipt, nil
	}})
	cookie, csrf := authenticateBrowser(t, handler)
	request := browserRequest(http.MethodPost, "/app/api/v1/workboards/board-a/operations",
		`{"version":1,"action":"card.cancel_request","idempotency_key":"browser-workboard-key-01","board_id":"board-a","card_id":"card-a","expected_card_revision":6}`)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || got.Action != contract.CardCancelRequest || got.BoardID != "board-a" || got.CardID != "card-a" ||
		got.ExpectedCardRevision == nil || *got.ExpectedCardRevision != 6 {
		t.Fatalf("status=%d request=%+v body=%s", response.Code, got, response.Body.String())
	}
}

func TestBrowserAcceptanceMutationPreservesClosedEvidenceFences(t *testing.T) {
	var got contract.BoardRequest
	handler := browserWorkboardHandler(t, WorkboardServices{Mutate: func(_ context.Context, _ string, input contract.BoardRequest) (contract.OperationReceipt, error) {
		got = input
		receipt := validBrowserReceipt(input.BoardID)
		next := *input.ExpectedCardRevision + 1
		receipt.CardID, receipt.CardRevision, receipt.BoardRevision = input.CardID, &next, 12
		return receipt, nil
	}})
	cookie, csrf := authenticateBrowser(t, handler)
	digest := strings.Repeat("a", 64)
	request := browserRequest(http.MethodPost, "/app/api/v1/workboards/board-a/operations",
		`{"version":1,"action":"acceptance.accept","idempotency_key":"browser-acceptance-key-01","board_id":"board-a","card_id":"card-a","attempt_id":"attempt-a","candidate_id":"candidate-a","criteria_revision":3,"expected_card_revision":6,"evidence_head_revision":2,"evidence":"Reviewed deterministic tests and the subjective criterion.","candidate_digest":"`+digest+`","criteria_digest":"`+digest+`","evidence_set_digest":"`+digest+`","policy_digest":"`+digest+`"}`)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || got.Action != contract.AcceptanceAccept || got.BoardID != "board-a" || got.CardID != "card-a" ||
		got.AttemptID != "attempt-a" || got.CandidateID != "candidate-a" || got.ExpectedCardRevision == nil || *got.ExpectedCardRevision != 6 ||
		got.CriteriaRevision == nil || *got.CriteriaRevision != 3 || got.EvidenceHeadRevision == nil || *got.EvidenceHeadRevision != 2 ||
		got.CandidateDigest != digest || got.CriteriaDigest != digest || got.EvidenceSetDigest != digest || got.PolicyDigest != digest {
		t.Fatalf("status=%d request=%+v body=%s", response.Code, got, response.Body.String())
	}
}

func TestBrowserWorkboardRejectsWorkerActionBeforeCallback(t *testing.T) {
	var calls atomic.Int32
	handler := browserWorkboardHandler(t, WorkboardServices{Mutate: func(context.Context, string, contract.BoardRequest) (contract.OperationReceipt, error) {
		calls.Add(1)
		return contract.OperationReceipt{}, nil
	}})
	cookie, csrf := authenticateBrowser(t, handler)
	revision := int64(1)
	body, _ := json.Marshal(contract.BoardRequest{Version: 1, Action: contract.CardClaim, IdempotencyKey: browserWorkboardKey,
		BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision})
	request := browserRequest(http.MethodPost, "/app/api/v1/workboards/board-a/operations", string(body))
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("worker action crossed browser operator boundary: status=%d calls=%d", response.Code, calls.Load())
	}
}

func TestBrowserWorkboardErrorsAndMethodsAreSanitized(t *testing.T) {
	handler := browserWorkboardHandler(t, WorkboardServices{Read: func(context.Context, string, string, contract.BoardSnapshotOptions) (contract.BoardSnapshot, error) {
		return contract.BoardSnapshot{}, errors.New("private path /secret and key")
	}})
	cookie, csrf := authenticateBrowser(t, handler)
	request := browserWorkboardGET("/app/api/v1/workboards/board-a")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private") || !json.Valid(response.Body.Bytes()) {
		t.Fatal(response.Code, response.Body.String())
	}

	request = browserRequest(http.MethodPut, "/app/api/v1/workboards", `{}`)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, POST" {
		t.Fatal(response.Code, response.Body.String())
	}

	request = browserRequest(http.MethodPost, "/app/api/v1/workboards/board-a", `{}`)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatal(response.Code, response.Body.String())
	}
	request = browserRequest(http.MethodPost, "/app/api/v1/workboards/board-a", `{}`)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	request.Header.Set("Origin", "http://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("foreign-origin method probe accepted", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, browserWorkboardGET("/app/api/v1/workboards/board-a/operations"))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Allow") != "" {
		t.Fatal("unauthenticated operation method probe disclosed route", response.Code, response.Header().Get("Allow"))
	}

	request = browserRequest(http.MethodPut, "/app/api/v1/workboards/board-a/operations", `{}`)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	request.Header.Set("Origin", "http://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Header().Get("Allow") != "" {
		t.Fatal("foreign-origin operation method probe disclosed route", response.Code, response.Header().Get("Allow"))
	}

	request = browserRequest(http.MethodPut, "/app/api/v1/workboards/board-a/operations", `{}`)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
		t.Fatal("authorized operation method probe did not receive method contract", response.Code, response.Header().Get("Allow"))
	}
}

func TestBrowserWorkboardAmbiguousErrorPublishesOnlyJournalCorrelation(t *testing.T) {
	var operationID string
	handler := browserWorkboardHandler(t, WorkboardServices{Mutate: func(_ context.Context, subject string, input contract.BoardRequest) (contract.OperationReceipt, error) {
		digest := sha256.Sum256([]byte(subject + "\x00" + input.IdempotencyKey))
		operationID = "op_" + hex.EncodeToString(digest[:])
		return contract.OperationReceipt{}, &app.BrowserOperationError{OperationID: operationID, Cause: errors.New("private request and receipt body")}
	}})
	cookie, csrf := authenticateBrowser(t, handler)
	title := "Sensitive board title"
	body, _ := json.Marshal(contract.BoardRequest{Version: 1, Action: contract.BoardCreate, IdempotencyKey: browserWorkboardKey, Title: &title})
	request := browserRequest(http.MethodPost, "/app/api/v1/workboards", string(body))
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var published contract.Error
	if response.Code != http.StatusServiceUnavailable || json.Unmarshal(response.Body.Bytes(), &published) != nil || published.Validate() != nil ||
		published.OperationID != operationID || !published.Retryable || published.Code != "workboard_unavailable" {
		t.Fatalf("status=%d error=%+v body=%s", response.Code, published, response.Body.String())
	}
	for _, secret := range []string{title, browserWorkboardKey, "private request", "receipt body"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("browser error leaked %q: %s", secret, response.Body.String())
		}
	}
}
