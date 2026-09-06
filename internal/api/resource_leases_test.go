package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func resourceLeaseFixture() workers.ScopeLeaseStatus {
	return workers.ScopeLeaseStatus{Version: 1, OverlapPolicyVersion: 1, Scope: "workspace", ObservedAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), StorageSchema: 23, Available: true, Holders: []workers.ScopeLeaseHolder{{TaskID: "task", LiveReaders: 1, ExpiredReaders: 2}}}
}

func TestResourceLeasesMetadataAndCapacity(t *testing.T) {
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("inspection executed task")
		return app.Result{}, nil
	}
	calls := 0
	s.ScopeLeases = func(ctx context.Context, scope string) (workers.ScopeLeaseStatus, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if scope != "workspace" || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
			t.Fatal("unbounded or misbound inspection")
		}
		return resourceLeaseFixture(), nil
	}
	h, _ := New(token, 1, s)
	h.slots <- struct{}{}
	for range 2 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/leases?scope=workspace", ""))
		var got workers.ScopeLeaseStatus
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, resourceLeaseFixture()) {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || len(h.controls) != 0 || len(h.slots) != 1 {
			t.Fatal("inspection capacity or caching")
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestResourceLeasesAdmission(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "missing", "empty", "duplicate", "unknown", "malformed", "utf8", "leading_space", "trailing_space", "control", "too_long", "bare_query", "body", "unknown_length", "transfer", "hidden_body", "method", "missing_hook", "capacity", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			calls := 0
			s.ScopeLeases = func(context.Context, string) (workers.ScopeLeaseStatus, error) {
				calls++
				return resourceLeaseFixture(), nil
			}
			if mode == "missing_hook" {
				s.ScopeLeases = nil
			}
			h, _ := New(token, 1, s)
			r := continuationRequest("GET", "/v1/resources/leases?scope=workspace", "")
			want := 400
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				r.URL.RawQuery = "private=%ZZ"
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				r.URL.RawQuery = "private=%ZZ"
				want = 403
			case "missing":
				r.URL.RawQuery = ""
			case "empty":
				r.URL.RawQuery = "scope="
			case "duplicate":
				r.URL.RawQuery = "scope=workspace&scope=workspace"
			case "unknown":
				r.URL.RawQuery = "scope=workspace&private=value"
			case "malformed":
				r.URL.RawQuery = "scope=%ZZ"
			case "utf8":
				r.URL.RawQuery = "scope=%FF"
			case "leading_space":
				r.URL.RawQuery = "scope=+workspace"
			case "trailing_space":
				r.URL.RawQuery = "scope=workspace+"
			case "control":
				r.URL.RawQuery = "scope=work%00space"
			case "too_long":
				r.URL.RawQuery = "scope=" + strings.Repeat("a", 513)
			case "bare_query":
				r.URL.RawQuery = ""
				r.URL.ForceQuery = true
			case "body":
				r.Body = leaseUnreadBody{t}
				r.ContentLength = 1
			case "unknown_length":
				r.Body = leaseUnreadBody{t}
				r.ContentLength = -1
			case "transfer":
				r.Body = leaseUnreadBody{t}
				r.TransferEncoding = []string{"chunked"}
			case "hidden_body":
				r.Body = leaseUnreadBody{t}
				r.ContentLength = 0
			case "method":
				r.Method = "POST"
				want = 405
			case "missing_hook":
				want = 503
			case "capacity":
				h.controls <- struct{}{}
				h.controls <- struct{}{}
				want = 503
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
				want = 503
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestResourceLeasesBackendValidation(t *testing.T) {
	for _, mode := range []string{"error", "panic", "scope", "version", "overlap", "time", "schema", "available", "nil_holders", "negative", "overflow", "empty_holder", "duplicate", "unsorted", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.ScopeLeases = func(context.Context, string) (workers.ScopeLeaseStatus, error) {
				out := resourceLeaseFixture()
				switch mode {
				case "error":
					return out, errors.New("private backend payload")
				case "panic":
					panic("private backend payload")
				case "scope":
					out.Scope = "private backend payload"
				case "version":
					out.Version = 2
				case "overlap":
					out.OverlapPolicyVersion = 2
				case "time":
					out.ObservedAt = time.Time{}
				case "schema":
					out.StorageSchema = 30
				case "available":
					out.Available = false
				case "nil_holders":
					out.Holders = nil
				case "negative":
					out.Holders[0].LiveReaders = -1
				case "overflow":
					out.Holders[0].LiveReaders = 1<<63 - 1
				case "empty_holder":
					out.Holders[0].TaskID = ""
				case "duplicate":
					out.Holders = append(out.Holders, out.Holders[0])
				case "unsorted":
					out.Holders = append(out.Holders, workers.ScopeLeaseHolder{TaskID: "aaa", LiveReaders: 1})
				case "canceled":
					cancel()
				}
				return out, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/leases?scope=workspace", "").WithContext(ctx))
			want := 500
			if mode == "error" || mode == "panic" || mode == "canceled" {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "holders") || len(h.controls) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestResourceLeasesReadOnlyMethods(t *testing.T) {
	s := services()
	s.ScopeLeases = func(context.Context, string) (workers.ScopeLeaseStatus, error) {
		t.Fatal("method reached backend")
		return workers.ScopeLeaseStatus{}, nil
	}
	h, _ := New(token, 1, s)
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH", "HEAD"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest(method, "/v1/resources/leases?scope=workspace", ""))
		if w.Code != 405 || w.Header().Get("Allow") != "GET" {
			t.Fatal(method, w.Code, w.Body.String())
		}
	}
}

func TestResourceLeasesLegacyAvailability(t *testing.T) {
	for _, version := range []int{1, 2, 3, 22, 23} {
		s := services()
		s.ScopeLeases = func(context.Context, string) (workers.ScopeLeaseStatus, error) {
			out := resourceLeaseFixture()
			out.StorageSchema = version
			out.Available = version >= 3
			out.Holders = []workers.ScopeLeaseHolder{}
			return out, nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/leases?scope=workspace", ""))
		var out workers.ScopeLeaseStatus
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Available != (version >= 3) || out.Holders == nil {
			t.Fatal(version, w.Code, w.Body.String())
		}
	}
}

func TestResourceLeasesScopeQueryBoundaries(t *testing.T) {
	for _, scope := range []string{"workspace", "scope with internal spaces", "日本語", strings.Repeat("a", 512)} {
		got, err := leaseScopeQuery("scope=" + url.QueryEscape(scope))
		if err != nil || got != scope {
			t.Fatal("valid decoded scope rejected", err)
		}
	}
	for _, raw := range []string{"scope=" + strings.Repeat("x", 2049), "scope=workspace;unknown=x", "scope=workspace&%73cope=workspace", "scope=%E2%80%83workspace", "scope=workspace%E2%80%83", "scope=work%C2%85space"} {
		if _, err := leaseScopeQuery(raw); err == nil {
			t.Fatal("invalid query admitted")
		}
	}
}
