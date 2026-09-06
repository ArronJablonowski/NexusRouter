package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func memoryExportResponseFixture() memory.ExportSnapshot {
	first := managedMemoryFact()
	first.ID = "a-private-expired"
	first.Expires = first.Created.Add(time.Hour)
	second := managedMemoryFact()
	second.ID = "b-shareable"
	second.Privacy = "shareable"
	return memory.ExportSnapshot{Version: 1, Scope: first.Scope, CapturedAt: time.Now().UTC(), Facts: []memory.Fact{first, second}}
}

func TestMemoryExportHTTPReturnsCompleteVersionedSnapshot(t *testing.T) {
	want := memoryExportResponseFixture()
	calls := 0
	s := services()
	s.ExportMemory = func(ctx context.Context) (memory.ExportSnapshot, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
			t.Error("unbounded export context")
		}
		return want, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/memory/export", `{"version":1}`))
	var got memory.ExportSnapshot
	if w.Code != 200 || calls != 1 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("snapshot filtered or changed", w.Code, w.Body.String(), calls)
	}
	if got.Validate() != nil {
		t.Fatal("invalid successful snapshot")
	}
}

func TestMemoryExportHTTPAdmission(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "method", "query", "forcequery", "media", "nil-body", "oversize", "transfer", "unknown-length", "missing", "duplicate", "escaped-duplicate", "case", "null", "extra", "scope", "filter", "string-version", "fraction", "zero-version", "trailing", "utf8", "surrogate", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			s := services()
			s.ExportMemory = func(context.Context) (memory.ExportSnapshot, error) {
				calls++
				return memoryExportResponseFixture(), nil
			}
			h, _ := New(token, 1, s)
			body := `{"version":1}`
			want := 400
			switch mode {
			case "missing":
				body = `{}`
			case "duplicate":
				body = `{"version":1,"version":1}`
			case "escaped-duplicate":
				body = `{"version":1,"\u0076ersion":1}`
			case "case":
				body = `{"Version":1}`
			case "null":
				body = `{"version":null}`
			case "extra":
				body = `{"version":1,"extra":true}`
			case "scope":
				body = `{"version":1,"scope":"other"}`
			case "filter":
				body = `{"version":1,"include_expired":false}`
			case "string-version":
				body = `{"version":"1"}`
			case "fraction":
				body = `{"version":1.5}`
			case "zero-version":
				body = `{"version":0}`
			case "trailing":
				body += ` {}`
			case "utf8":
				body += string([]byte{255})
			case "surrogate":
				body = `{"version":1,"\ud800":true}`
			case "oversize":
				body = strings.Repeat("x", memoryBodyLimit+1)
				want = 413
			}
			r := request("POST", "/v1/memory/export", body)
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "method":
				r.Method = "GET"
				want = 405
			case "query":
				r.URL.RawQuery = "scope=other"
			case "forcequery":
				r.URL.ForceQuery = true
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = 415
			case "nil-body":
				r.Body = nil
				want = 413
			case "transfer":
				r.TransferEncoding = []string{"chunked"}
			case "unknown-length":
				r.ContentLength = -1
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
				want = 503
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 {
				t.Fatal(mode, w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestMemoryExportHTTPBackendBoundary(t *testing.T) {
	for _, mode := range []string{"nil", "error", "conflict", "panic", "canceled", "version", "nil-facts", "scope", "fact-scope", "order", "duplicate", "bad-fact", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := services()
			calls := 0
			if mode != "nil" {
				s.ExportMemory = func(context.Context) (memory.ExportSnapshot, error) {
					calls++
					out := memoryExportResponseFixture()
					out.Facts[0].Content = "PRIVATE_BACKEND_OUTPUT"
					switch mode {
					case "error":
						return out, errors.New("PRIVATE_BACKEND_ERROR")
					case "conflict":
						return out, memory.ErrConflict
					case "panic":
						panic("PRIVATE_BACKEND_PANIC")
					case "canceled":
						cancel()
					case "version":
						out.Version = 2
					case "nil-facts":
						out.Facts = nil
					case "scope":
						out.Scope = ""
					case "fact-scope":
						out.Facts[1].Scope = "other"
					case "order":
						out.Facts[0], out.Facts[1] = out.Facts[1], out.Facts[0]
					case "duplicate":
						out.Facts[1].ID = out.Facts[0].ID
					case "bad-fact":
						out.Facts[1].Confidence = 2
					case "oversize":
						out.Facts = nil
						for i := 0; i < 140; i++ {
							f := managedMemoryFact()
							f.ID = fmt.Sprintf("fact-%03d", i)
							f.Content = strings.Repeat("x", 65536)
							out.Facts = append(out.Facts, f)
						}
					}
					return out, nil
				}
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/memory/export", `{"version":1}`).WithContext(ctx))
			wantCalls := 1
			if mode == "nil" {
				wantCalls = 0
			}
			if w.Code != 503 || calls != wantCalls || strings.Contains(w.Body.String(), "PRIVATE_BACKEND") || strings.Contains(w.Body.String(), "facts") {
				t.Fatal("unsafe export response", w.Code, w.Body.String(), calls)
			}
		})
	}
}

type memoryExportDeadlineWriter struct {
	*httptest.ResponseRecorder
	mode      string
	deadlines []time.Time
	writes    int
}

func (w *memoryExportDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	if w.mode == "reset-panic" && deadline.IsZero() {
		panic("PRIVATE_RESET_PANIC")
	}
	if w.mode == "deadline-error" && !deadline.IsZero() {
		return errors.New("PRIVATE_DEADLINE_ERROR")
	}
	return nil
}

func (w *memoryExportDeadlineWriter) Write(body []byte) (int, error) {
	w.writes++
	switch w.mode {
	case "partial-panic":
		w.ResponseRecorder.Write(body[:len(body)/2])
		panic("PRIVATE_WRITE_PANIC")
	case "short":
		return w.ResponseRecorder.Write(body[:len(body)/2])
	case "write-error":
		return 0, io.ErrClosedPipe
	default:
		return w.ResponseRecorder.Write(body)
	}
}

func TestMemoryExportHTTPOutputDeadlineAndNoRetry(t *testing.T) {
	for _, mode := range []string{"success", "deadline-error", "short", "write-error", "partial-panic", "reset-panic"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			s.ExportMemory = func(context.Context) (memory.ExportSnapshot, error) { return memoryExportResponseFixture(), nil }
			h, _ := New(token, 1, s)
			w := &memoryExportDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), mode: mode}
			h.ServeHTTP(w, request("POST", "/v1/memory/export", `{"version":1}`))
			if len(w.deadlines) == 0 || w.deadlines[0].IsZero() || time.Until(w.deadlines[0]) > 15*time.Second {
				t.Fatal("write lacked bounded deadline")
			}
			if mode == "deadline-error" {
				if w.Code != 503 || strings.Contains(w.Body.String(), "facts") || strings.Contains(w.Body.String(), "PRIVATE") {
					t.Fatal("failed deadline published snapshot", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != 200 || w.writes != 1 || len(w.deadlines) != 2 || !w.deadlines[1].IsZero() {
				t.Fatal("write retried or deadline not reset", w.Code, w.writes, w.deadlines)
			}
			length, err := strconv.Atoi(w.Header().Get("Content-Length"))
			if err != nil || length < 1 {
				t.Fatal("snapshot content length missing")
			}
			if mode == "success" || mode == "reset-panic" {
				var snapshot memory.ExportSnapshot
				if json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || snapshot.Validate() != nil || length != w.Body.Len() {
					t.Fatal("invalid successful output")
				}
			}
			if (mode == "short" || mode == "partial-panic") && (strings.Contains(w.Body.String(), "memory_unavailable") || strings.Contains(w.Body.String(), "PRIVATE_") || w.Body.Len() >= length) {
				t.Fatal("error appended to partial snapshot")
			}
		})
	}
}

func TestMemoryExportHTTPSharesMemoryCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	var exports, gets atomic.Int32
	block := func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s := services()
	s.Memory = func(ctx context.Context, _ string) (memory.Fact, error) {
		gets.Add(1)
		return managedMemoryFact(), block(ctx)
	}
	s.ExportMemory = func(ctx context.Context) (memory.ExportSnapshot, error) {
		exports.Add(1)
		return memoryExportResponseFixture(), block(ctx)
	}
	h, _ := New(token, 1, s)
	finished := make(chan int, 2)
	for _, action := range []string{"get", "export"} {
		go func(action string) {
			body := `{"version":1}`
			if action == "get" {
				body = managedMemoryBody("get")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/memory/"+action, body).WithContext(ctx))
			finished <- w.Code
		}(action)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("memory operations did not acquire shared capacity")
		}
	}
	for _, action := range []string{"export", "get"} {
		body := `{"version":1}`
		if action == "get" {
			body = managedMemoryBody("get")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/memory/"+action, body))
		if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
			t.Fatal("memory capacity not shared", action, w.Code)
		}
	}
	if exports.Load() != 1 || gets.Load() != 1 {
		t.Fatal("capacity rejection invoked backend")
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case code := <-finished:
			if code != 503 {
				t.Fatal("canceled blocked backend succeeded", code)
			}
		case <-time.After(time.Second):
			t.Fatal("blocked operation did not join")
		}
	}
}
