package webuiapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/browserauth"
	"github.com/ArronJablonowski/NexusRouter/internal/browserops"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func mutationHandlerFixture(t *testing.T, services MutationServices) *Handler {
	t.Helper()
	store, err := browserauth.New(browserauth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{BasePath: "/app", AllowedHosts: []string{"127.0.0.1:7788"}, Store: store, Mutations: services})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func authorizedMutationRequest(t *testing.T, handler *Handler, target, body string) *http.Request {
	t.Helper()
	cookie, csrf := authenticateBrowser(t, handler)
	request := browserRequest(http.MethodPost, target, body)
	request.AddCookie(cookie)
	request.Header.Set("X-Darwin-CSRF", csrf)
	return request
}

func TestChatSubmitRequiresBrowserAuthorityAndClosedAction(t *testing.T) {
	var calls atomic.Int32
	handler := mutationHandlerFixture(t, MutationServices{Chat: func(_ context.Context, subject string, request contract.ChatRequest) (contract.ChatMutationReceipt, error) {
		if len(subject) != 64 {
			t.Fatal("missing session subject")
		}
		calls.Add(1)
		return contract.ChatMutationReceipt{Version: 1, OperationID: "operation_123456789", SubmissionID: "submission_123456789", State: "queued"}, nil
	}})
	body := `{"version":1,"action":"submit","idempotency_key":"browser-submit-key-01","text":"hello"}`
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, browserRequest(http.MethodPost, "/app/api/v1/chats", body))
	if unauthorized.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("unauthorized mutation dispatched", unauthorized.Code, calls.Load())
	}
	wrong := authorizedMutationRequest(t, handler, "/app/api/v1/chats", `{"version":1,"action":"resume","idempotency_key":"browser-submit-key-01","chat_id":"chat","task_id":"task","text":"hello","expected_revision":1}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, wrong)
	if response.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatal("wrong action dispatched", response.Code, calls.Load())
	}
	request := authorizedMutationRequest(t, handler, "/app/api/v1/chats", body)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var receipt contract.ChatMutationReceipt
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &receipt) != nil || receipt.Validate() != nil || calls.Load() != 1 {
		t.Fatal(response.Code, response.Body.String(), calls.Load())
	}
}

func TestMutationPathIdentityAndProjectionAuthentication(t *testing.T) {
	var cancelCalls atomic.Int32
	handler := mutationHandlerFixture(t, MutationServices{
		Cancel: func(_ context.Context, _ string, request contract.ChatRequest) (contract.CancellationReceipt, error) {
			cancelCalls.Add(1)
			revision := int64(2)
			return contract.CancellationReceipt{Version: 1, OperationID: "operation_123456789", TargetKind: "task", TargetID: request.TaskID, State: "running", Requested: true, RequestID: "request_123456789", RequestedAt: nil, Revision: &revision}, nil
		},
		TaskControls: func(_ context.Context, task string) (contract.TaskControlStatus, error) {
			return contract.TaskControlStatus{Version: 1, TaskID: task, Revision: 2, CanCancel: true}, nil
		},
	})
	body := `{"version":1,"action":"cancel","idempotency_key":"browser-cancel-key-01","task_id":"different","expected_revision":1}`
	request := authorizedMutationRequest(t, handler, "/app/api/v1/tasks/task/cancel", body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || cancelCalls.Load() != 0 {
		t.Fatal("foreign path identity dispatched", response.Code, cancelCalls.Load())
	}
	read := browserRequest(http.MethodGet, "/app/api/v1/tasks/task/controls", "")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, read)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated control projection", response.Code)
	}
}

func TestOperationAndSubmissionReadsAreAuthenticatedAndBounded(t *testing.T) {
	now := time.Now().UTC()
	handler := mutationHandlerFixture(t, MutationServices{
		Operations: func(_ context.Context, subject, after string, limit int) (contract.OperationPage, error) {
			if len(subject) != 64 || after != "" || limit != 1 {
				t.Fatal("bad operation projection binding", subject, after, limit)
			}
			return contract.OperationPage{Version: 1, Items: []contract.OperationSummary{{Version: 1, OperationID: "operation_123456789", Action: "submit", State: "pending", CreatedAt: now, UpdatedAt: now}}}, nil
		},
		Submission: func(_ context.Context, subject, id string) (contract.SubmissionStatus, error) {
			if len(subject) != 64 {
				t.Fatal("missing submission subject")
			}
			return contract.SubmissionStatus{Version: 1, SubmissionID: id, State: "queued", CreatedAt: now, UpdatedAt: now, CanCancel: true}, nil
		},
	})
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, browserRequest(http.MethodGet, "/app/api/v1/operations?limit=1", ""))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatal(unauthorized.Code)
	}
	cookie, _ := authenticateBrowser(t, handler)
	request := browserRequest(http.MethodGet, "/app/api/v1/operations?limit=1", "")
	request.Body, request.ContentLength = http.NoBody, 0
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	request = browserRequest(http.MethodGet, "/app/api/v1/submissions/submission_123456789", "")
	request.Body, request.ContentLength = http.NoBody, 0
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestRevisionConflictPublishesTypedCurrentRevision(t *testing.T) {
	handler := mutationHandlerFixture(t, MutationServices{
		Steer: func(context.Context, string, contract.ChatRequest) (contract.SteeringReceipt, error) {
			return contract.SteeringReceipt{}, &app.BrowserOperationError{OperationID: "operation_123456789", Cause: browserops.ErrConflict}
		},
		TaskControls: func(_ context.Context, task string) (contract.TaskControlStatus, error) {
			return contract.TaskControlStatus{Version: 1, TaskID: task, Revision: 7}, nil
		},
	})
	body := `{"version":1,"action":"steer","idempotency_key":"browser-steer-key-01","task_id":"task","text":"next","expected_revision":2}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authorizedMutationRequest(t, handler, "/app/api/v1/tasks/task/steering", body))
	var published contract.Error
	if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &published) != nil || published.Validate() != nil || published.CurrentRevision == nil || *published.CurrentRevision != 7 || published.SubjectType != "task" || published.SubjectID != "task" || published.OperationID != "operation_123456789" {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestInvalidSubmissionIsDefinitiveAdmissionRejection(t *testing.T) {
	handler := mutationHandlerFixture(t, MutationServices{Chat: func(context.Context, string, contract.ChatRequest) (contract.ChatMutationReceipt, error) {
		return contract.ChatMutationReceipt{}, submissions.ErrInvalid
	}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authorizedMutationRequest(t, handler, "/app/api/v1/chats", `{"version":1,"action":"submit","idempotency_key":"browser-submit-key-01","text":"hello"}`))
	var failure contract.Error
	if response.Code != http.StatusUnprocessableEntity || json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure.Code != "admission_denied" || failure.Retryable {
		t.Fatalf("expected definite rejection, got %d %s", response.Code, response.Body.String())
	}
}
