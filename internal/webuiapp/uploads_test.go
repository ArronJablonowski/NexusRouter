package webuiapp

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadMutationAuthCSRFAndByteBounds(t *testing.T) {
	calls := 0
	h := mutationHandlerFixture(t, MutationServices{Chat: func(_ context.Context, subject string, r webui.ChatRequest) (webui.ChatMutationReceipt, error) {
		calls++
		if len(subject) != 64 || len(r.Files) != 1 || len(r.Files[0].Data) != (2<<20) {
			t.Fatal("file authority or byte count lost")
		}
		return webui.ChatMutationReceipt{Version: 1, OperationID: "uploaded-operation", SubmissionID: "uploaded-submission", State: "queued"}, nil
	}})
	request := webui.ChatRequest{Version: 1, Action: webui.ChatSubmit, IdempotencyKey: "upload-boundary-key-01", Text: "Read file", Files: []attachments.File{{Name: "large.txt", Data: bytes.Repeat([]byte("a"), 2<<20)}}}
	body, _ := json.Marshal(request)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, browserRequest(http.MethodPost, "/app/api/v1/chats", string(body)))
	if w.Code != 401 || calls != 0 {
		t.Fatal("anonymous upload", w.Code)
	}
	r := authorizedMutationRequest(t, h, "/app/api/v1/chats", string(body))
	r.Header.Del("X-Darwin-CSRF")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || calls != 0 {
		t.Fatal("CSRF upload", w.Code)
	}
	r = authorizedMutationRequest(t, h, "/app/api/v1/chats", string(body))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 || calls != 1 {
		t.Fatal("bounded larger upload rejected", w.Code, w.Body.String())
	}
	bad := `{"version":1,"action":"submit","idempotency_key":"upload-boundary-key-02","text":"Read","files":[{"name":"file","data":"***"}]}`
	r = authorizedMutationRequest(t, h, "/app/api/v1/chats", bad)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 || calls != 1 {
		t.Fatal("invalid base64 dispatched", w.Code)
	}
}

func TestDocumentReaderFailureIsDefinitiveAndSafe(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{Chat: func(context.Context, string, webui.ChatRequest) (webui.ChatMutationReceipt, error) {
		return webui.ChatMutationReceipt{}, attachments.ErrProcessing
	}})
	r := authorizedMutationRequest(t, h, "/app/api/v1/chats", `{"version":1,"action":"submit","idempotency_key":"document-reader-failure-01","text":"Read","files":[{"name":"scan.pdf","data":"JVBERi0="}]}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var result webui.Error
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || w.Code != 422 || result.Retryable || result.Code != "attachment_processing_unavailable" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestUploadOriginalByteBoundary(t *testing.T) {
	calls := 0
	h := mutationHandlerFixture(t, MutationServices{Chat: func(_ context.Context, _ string, r webui.ChatRequest) (webui.ChatMutationReceipt, error) {
		calls++
		return webui.ChatMutationReceipt{Version: 1, OperationID: "op-bound", SubmissionID: "sub-bound", State: "queued"}, nil
	}})
	for _, size := range []int{attachments.MaxTotalBytes, attachments.MaxTotalBytes + 1} {
		body, _ := json.Marshal(webui.ChatRequest{Version: 1, Action: webui.ChatSubmit, IdempotencyKey: "original-size-boundary", Text: "Read", Files: []attachments.File{{Name: "file.bin", Data: bytes.Repeat([]byte("a"), size)}}})
		r := authorizedMutationRequest(t, h, "/app/api/v1/chats", string(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expected := 202
		if size > attachments.MaxTotalBytes {
			expected = 400
		}
		if w.Code != expected {
			t.Fatal(size, w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("oversized original reached mutation")
	}
}
