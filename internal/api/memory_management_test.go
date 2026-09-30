package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/memory"
)

func managedMemoryFact() memory.Fact {
	stamp := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return memory.Fact{Version: 1, ID: "fact", Scope: "project", Revision: 1, Content: "Prefers Go", Provenance: "operator", Confidence: 1, Privacy: "local_only", Created: stamp, Updated: stamp}
}

func managedMemoryBody(action string) string {
	switch action {
	case "get":
		return `{"version":1,"id":"fact"}`
	case "query":
		return `{"version":1,"after_id":"","contains":"","limit":10,"include_expired":false}`
	case "delete":
		return `{"version":1,"id":"fact","expected_revision":1}`
	default:
		body, _ := json.Marshal(managedMemoryFact())
		return `{"version":1,"fact":` + string(body) + `,"expected_revision":0}`
	}
}

func managedMemoryServices(t *testing.T, calls *int) Services {
	t.Helper()
	s := services()
	check := func(ctx context.Context) {
		*calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
			t.Fatal("missing bounded context")
		}
	}
	s.Memory = func(ctx context.Context, id string) (memory.Fact, error) {
		check(ctx)
		if id != "fact" {
			t.Fatal(id)
		}
		return managedMemoryFact(), nil
	}
	s.Memories = func(ctx context.Context, after, contains string, limit int, expired bool) ([]memory.Fact, error) {
		check(ctx)
		if after != "" || contains != "" || limit != 10 || expired {
			t.Fatal("query changed")
		}
		return []memory.Fact{managedMemoryFact()}, nil
	}
	s.PutMemory = func(ctx context.Context, f memory.Fact, expected int64) error {
		check(ctx)
		if f != managedMemoryFact() || expected != 0 {
			t.Fatal("fact changed")
		}
		return nil
	}
	s.DeleteMemory = func(ctx context.Context, id string, expected int64) error {
		check(ctx)
		if id != "fact" || expected != 1 {
			t.Fatal("delete changed")
		}
		return nil
	}
	return s
}

func TestMemoryManagementSuccess(t *testing.T) {
	for _, action := range []string{"get", "query", "put", "delete"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			h, _ := New(token, 1, managedMemoryServices(t, &calls))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/memory/"+action, managedMemoryBody(action)))
			if w.Code != 200 || calls != 1 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
			if action == "get" {
				var f memory.Fact
				if json.Unmarshal(w.Body.Bytes(), &f) != nil || f != managedMemoryFact() {
					t.Fatal(w.Body.String())
				}
			}
			if action == "query" {
				var page struct {
					Version int           `json:"version"`
					Facts   []memory.Fact `json:"facts"`
				}
				if json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Version != 1 || len(page.Facts) != 1 || page.Facts[0] != managedMemoryFact() {
					t.Fatal(w.Body.String())
				}
			}
		})
	}
}

func TestMemoryManagementAdmission(t *testing.T) {
	for _, action := range []string{"get", "query", "put", "delete"} {
		for _, mode := range []string{"auth", "origin", "query", "forcequery", "method", "media", "missing_hook", "capacity", "canceled", "oversize", "transfer", "unknown_length", "missing", "unknown", "duplicate", "escaped_duplicate", "version_null", "version_string", "version_fraction", "trailing", "utf8"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				calls := 0
				s := managedMemoryServices(t, &calls)
				if mode == "missing_hook" {
					s.Memory = nil
					s.Memories = nil
					s.PutMemory = nil
					s.DeleteMemory = nil
				}
				h, _ := New(token, 1, s)
				body := managedMemoryBody(action)
				want := 400
				switch mode {
				case "missing":
					body = `{}`
				case "unknown":
					body = strings.Replace(body, `"version":1`, `"unknown":1,"version":1`, 1)
				case "duplicate":
					body = strings.Replace(body, `"version":1`, `"version":1,"version":1`, 1)
				case "escaped_duplicate":
					body = strings.Replace(body, `"version":1`, `"version":1,"\u0076ersion":1`, 1)
				case "version_null":
					body = strings.Replace(body, `"version":1`, `"version":null`, 1)
				case "version_string":
					body = strings.Replace(body, `"version":1`, `"version":"1"`, 1)
				case "version_fraction":
					body = strings.Replace(body, `"version":1`, `"version":1.5`, 1)
				case "trailing":
					body += ` {}`
				case "utf8":
					body += string([]byte{255})
				case "oversize":
					body = strings.Repeat("x", (128<<10)+1)
					want = 413
				}
				r := request("POST", "/v1/memory/"+action, body)
				switch mode {
				case "auth":
					r.Header.Del("Authorization")
					want = 401
				case "origin":
					r.Header.Set("Origin", "https://example.com")
					want = 403
				case "query":
					r.URL.RawQuery = "x=y"
				case "forcequery":
					r.URL.ForceQuery = true
				case "method":
					r.Method = "GET"
					want = 405
				case "media":
					r.Header.Set("Content-Type", "text/plain")
					want = 415
				case "missing_hook":
					want = 503
				case "capacity":
					h.memorySlots <- struct{}{}
					h.memorySlots <- struct{}{}
					want = 503
				case "canceled":
					ctx, cancel := context.WithCancel(r.Context())
					cancel()
					r = r.WithContext(ctx)
					want = 503
				case "transfer":
					r.TransferEncoding = []string{"chunked"}
				case "unknown_length":
					r.ContentLength = -1
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want || calls != 0 {
					t.Fatal(w.Code, w.Body.String(), calls)
				}
			})
		}
	}
}

func TestMemoryManagementSpecificInputShapes(t *testing.T) {
	for _, tc := range []struct{ action, from, to string }{
		{"get", `"id":"fact"`, `"id":null`}, {"get", `"id":"fact"`, `"ID":"fact"`}, {"get", `"id":"fact"`, `"id":"bad\nkey"`},
		{"query", `"include_expired":false`, `"include_expired":null`}, {"query", `"include_expired":false`, `"include_expired":"false"`}, {"query", `,"include_expired":false`, ``}, {"query", `"after_id":""`, `"after_id":null`}, {"query", `"contains":""`, `"contains":null`}, {"query", `"limit":10`, `"limit":0`}, {"query", `"limit":10`, `"limit":1001`}, {"query", `"limit":10`, `"limit":1.5`},
		{"put", `"revision":1`, `"revision":2`}, {"put", `"revision":1`, `"revision":1,"revision":1`}, {"put", `"revision":1`, `"revision":1,"\u0072evision":1`}, {"put", `"content":"Prefers Go"`, `"Content":"Prefers Go"`}, {"put", `"content":"Prefers Go"`, `"content":null`}, {"put", `"content":"Prefers Go"`, `"extra":true,"content":"Prefers Go"`}, {"put", `"last_use":"0001-01-01T00:00:00Z"`, `"last_use":null`}, {"put", `"expected_revision":0`, `"expected_revision":-1`}, {"put", `"expected_revision":0`, `"expected_revision":null`},
		{"delete", `"expected_revision":1`, `"expected_revision":0`}, {"delete", `"expected_revision":1`, `"expected_revision":null`}, {"delete", `"expected_revision":1`, `"expected_revision":1.5`},
	} {
		t.Run(tc.action+"/"+tc.to, func(t *testing.T) {
			body := managedMemoryBody(tc.action)
			if !strings.Contains(body, tc.from) {
				t.Fatal("fixture field absent", tc.from)
			}
			body = strings.Replace(body, tc.from, tc.to, 1)
			calls := 0
			h, _ := New(token, 1, managedMemoryServices(t, &calls))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/memory/"+tc.action, body))
			if w.Code != 400 || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestMemoryManagementBackendFailures(t *testing.T) {
	for _, action := range []string{"get", "query", "put", "delete"} {
		for _, mode := range []string{"error", "panic", "conflict", "canceled"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s := services()
				fail := func() error {
					switch mode {
					case "panic":
						panic("PRIVATE_BACKEND")
					case "conflict":
						return memory.ErrConflict
					case "canceled":
						cancel()
						return nil
					}
					return errors.New("PRIVATE_BACKEND")
				}
				s.Memory = func(context.Context, string) (memory.Fact, error) { return managedMemoryFact(), fail() }
				s.Memories = func(context.Context, string, string, int, bool) ([]memory.Fact, error) {
					return []memory.Fact{managedMemoryFact()}, fail()
				}
				s.PutMemory = func(context.Context, memory.Fact, int64) error { return fail() }
				s.DeleteMemory = func(context.Context, string, int64) error { return fail() }
				h, _ := New(token, 1, s)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, request("POST", "/v1/memory/"+action, managedMemoryBody(action)).WithContext(ctx))
				want := 503
				if mode == "conflict" {
					want = 409
				}
				if w.Code != want || strings.Contains(w.Body.String(), "PRIVATE_BACKEND") {
					t.Fatal(w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestMemoryManagementMalformedBackend(t *testing.T) {
	for _, mode := range []string{"invalid_fact", "unknown_privacy", "invalid_utf8", "nil_page", "duplicate", "unsorted", "too_many"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			action := "get"
			f := managedMemoryFact()
			switch mode {
			case "invalid_fact":
				f.Version = 2
			case "unknown_privacy":
				f.Privacy = "PRIVATE_BACKEND"
			case "invalid_utf8":
				f.Content = string([]byte{255})
			default:
				action = "query"
			}
			s.Memory = func(context.Context, string) (memory.Fact, error) { return f, nil }
			s.Memories = func(context.Context, string, string, int, bool) ([]memory.Fact, error) {
				switch mode {
				case "nil_page":
					return nil, nil
				case "duplicate":
					return []memory.Fact{f, f}, nil
				case "unsorted":
					g := f
					g.ID = "a"
					return []memory.Fact{f, g}, nil
				default:
					return make([]memory.Fact, 11), nil
				}
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/memory/"+action, managedMemoryBody(action)))
			want := 503
			if mode == "nil_page" {
				want = 200
			}
			if w.Code != want || strings.Contains(w.Body.String(), "PRIVATE_BACKEND") {
				t.Fatal(w.Code, w.Body.String())
			}
			if mode == "nil_page" && !strings.Contains(w.Body.String(), `"facts":[]`) {
				t.Fatal("nil page not normalized")
			}
		})
	}
}
