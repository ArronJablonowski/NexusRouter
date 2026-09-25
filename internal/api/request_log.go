package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RequestLog includes no URL parameters, query strings, headers, bodies, remote
// addresses, credentials, or raw errors. Failed admission can occur before a
// durable task exists, so this metadata complements the runtime journal.
type RequestLog struct {
	Version    int       `json:"version"`
	Kind       string    `json:"kind"`
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Route      string    `json:"route"`
	Status     int       `json:"status"`
	ErrorCode  string    `json:"error_code,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Bytes      int64     `json:"response_bytes"`
}

// WithRequestLog leaves request and authorization behavior unchanged. Logging
// failures cannot fail a request, and handler panics retain net/http semantics.
func WithRequestLog(next http.Handler, emit func(RequestLog)) http.Handler {
	if emit == nil {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		start := time.Now()
		response := &loggedResponse{ResponseWriter: writer}
		completed := false
		defer func() {
			status := response.status
			if status == 0 {
				status = http.StatusOK
			}
			code := ""
			if !completed {
				code = "handler_interrupted"
				if response.status == 0 {
					status = http.StatusInternalServerError
				}
			} else if status >= 400 {
				code = "http_" + strconv.Itoa(status)
			}
			entry := RequestLog{Version: 1, Kind: "http.request.completed", Time: time.Now().UTC(),
				Method: safeRequestMethod(request.Method), Route: safeRequestRoute(request.URL.Path),
				Status: status, ErrorCode: code, DurationMS: time.Since(start).Milliseconds(), Bytes: response.bytes}
			// A local diagnostic sink is never an execution-authority boundary.
			func() {
				defer func() { _ = recover() }()
				emit(entry)
			}()
		}()
		var target http.ResponseWriter = response
		if taskStreamWriter(writer) {
			target = &loggedFlushingResponse{loggedResponse: response}
		}
		next.ServeHTTP(target, request)
		completed = true
	})
}

// JSONRequestLog serializes bounded metadata lines to an existing local daemon
// log destination. It never creates a new file or an additional content store.
func JSONRequestLog(writer io.Writer) func(RequestLog) {
	var mu sync.Mutex
	return func(entry RequestLog) {
		body, err := json.Marshal(entry)
		if err != nil || len(body) > 2048 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = writer.Write(append(body, '\n'))
	}
}

type loggedResponse struct {
	http.ResponseWriter
	status int
	bytes  int64
}

type loggedFlushingResponse struct{ *loggedResponse }

func (w *loggedFlushingResponse) FlushError() error {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *loggedFlushingResponse) Flush() { _ = w.FlushError() }

func (w *loggedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *loggedResponse) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggedResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += int64(n)
	return n, err
}

func safeRequestMethod(method string) string {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return method
	default:
		return "OTHER"
	}
}

func safeRequestRoute(path string) string {
	switch path {
	case "/health", "/v1/health", "/v1/tasks", "/v1/chat/completions", "/v1/models", "/v1/feedback", "/v1/feedback/revisions", "/v1/submissions", "/v1/metrics":
		return path
	}
	for _, prefix := range []string{"/v1/tasks/", "/v1/submissions/", "/v1/memory/", "/v1/skills/", "/v1/audits/"} {
		if strings.HasPrefix(path, prefix) {
			return prefix + "*"
		}
	}
	if strings.HasPrefix(path, "/v1/") {
		return "/v1/other"
	}
	return "other"
}
