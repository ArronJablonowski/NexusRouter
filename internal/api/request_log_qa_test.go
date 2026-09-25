package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type requestLogNonStreamingWriter struct{ http.ResponseWriter }

func TestRequestLogDoesNotInventFlushCapability(t *testing.T) {
	var entry RequestLog
	handler := WithRequestLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if taskStreamWriter(w) {
			t.Fatal("diagnostic wrapper invented streaming capability")
		}
		if err := http.NewResponseController(w).Flush(); !errors.Is(err, http.ErrNotSupported) {
			t.Fatalf("unsupported flush behavior changed: %v", err)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}), func(value RequestLog) { entry = value })
	response := httptest.NewRecorder()
	handler.ServeHTTP(requestLogNonStreamingWriter{ResponseWriter: response}, httptest.NewRequest("GET", "/v1/tasks/id/events", nil))
	if response.Code != http.StatusServiceUnavailable || entry.Status != response.Code || entry.ErrorCode != "http_503" {
		t.Fatalf("unsupported flush committed an implicit status: client=%d log=%+v", response.Code, entry)
	}
}

func TestRequestLogTracksStatusCommittedByControllerFlush(t *testing.T) {
	var entry RequestLog
	handler := WithRequestLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}), func(value RequestLog) { entry = value })
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/v1/tasks/id/events", nil))
	if response.Code != http.StatusOK || entry.Status != response.Code || entry.ErrorCode != "" {
		t.Fatalf("logged status diverged from the flushed HTTP response: client=%d log=%+v", response.Code, entry)
	}
}

func TestRequestLogTracksFlushedStatusWhenHandlerPanics(t *testing.T) {
	var entry RequestLog
	handler := WithRequestLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		panic("interrupted after flush")
	}), func(value RequestLog) { entry = value })
	response := httptest.NewRecorder()
	func() {
		defer func() {
			if recover() != "interrupted after flush" {
				t.Fatal("handler panic did not propagate unchanged")
			}
		}()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/v1/tasks/id/events", nil))
	}()
	if response.Code != http.StatusOK || entry.Status != response.Code || entry.ErrorCode != "handler_interrupted" {
		t.Fatalf("panic log invented an HTTP status after headers committed: client=%d log=%+v", response.Code, entry)
	}
}
