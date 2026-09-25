package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLogCapturesPreAdmissionFailureWithoutPrivateRequest(t *testing.T) {
	var output bytes.Buffer
	handler := WithRequestLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"private response"}`))
	}), JSONRequestLog(&output))
	request := httptest.NewRequest("POST", "/v1/tasks/private-task-id?token=private-query", strings.NewReader("private prompt"))
	request.Header.Set("Authorization", "Bearer private-credential")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var entry RequestLog
	if json.Unmarshal(output.Bytes(), &entry) != nil || entry.Status != 403 || entry.ErrorCode != "http_403" || entry.Route != "/v1/tasks/*" || entry.Method != "POST" || entry.Bytes == 0 || strings.Contains(output.String(), "private-") || strings.Contains(output.String(), "private ") {
		t.Fatal(output.String())
	}
	if response.Code != 403 || response.Body.String() != `{"error":"private response"}` {
		t.Fatal("logging changed HTTP response", response)
	}
}

func TestRequestLogPreservesStreamingUnwrapAndImplicitStatus(t *testing.T) {
	var entry RequestLog
	handler := WithRequestLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal("streaming capability lost", err)
		}
		// A later rejected header write must not change the observed status.
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("data: output\n\n"))
	}), func(value RequestLog) { entry = value })
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/v1/tasks/id/events", nil))
	if !response.Flushed || entry.Status != 200 || entry.ErrorCode != "" {
		t.Fatal(response.Flushed, entry)
	}
}

func TestRequestLogSinkPanicDoesNotChangeRequestAndHandlerPanicPropagates(t *testing.T) {
	handler := WithRequestLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), func(RequestLog) { panic("private sink failure") })
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/unknown/private", nil))
	if response.Code != 204 {
		t.Fatal("sink changed status", response.Code)
	}
	var entry RequestLog
	panicking := WithRequestLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private handler failure") }), func(value RequestLog) { entry = value })
	func() {
		defer func() {
			if recover() != "private handler failure" {
				t.Fatal("handler panic swallowed or replaced")
			}
		}()
		panicking.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/unknown/private", nil))
	}()
	if entry.Status != 500 || entry.ErrorCode != "handler_interrupted" || entry.Route != "other" {
		t.Fatal(entry)
	}
}
