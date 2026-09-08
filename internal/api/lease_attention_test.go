package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func attentionPageFixture() workers.LeaseAttentionPage {
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	return workers.LeaseAttentionPage{Version: 1, StorageSchema: 24, Available: true, Items: []workers.LeaseAttention{{Version: 1, ID: "attention-b", TaskID: "task", Writer: true, State: "open", Reason: "expired_unreleased", FirstObserved: now, UpdatedAt: now, LeaseExpires: now.Add(-time.Minute)}}}
}

func TestLeaseAttentionHTTPOptionsAndCapacity(t *testing.T) {
	for _, tc := range []struct {
		query   string
		options workers.LeaseAttentionOptions
	}{
		{"", workers.LeaseAttentionOptions{State: "open", Limit: 25}},
		{"?state=all&after=attention-a&limit=1", workers.LeaseAttentionOptions{State: "all", After: "attention-a", Limit: 1}},
		{"?state=resolved&limit=100", workers.LeaseAttentionOptions{State: "resolved", Limit: 100}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			s := services()
			calls := 0
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Fatal("inspection ran task")
				return app.Result{}, nil
			}
			want := attentionPageFixture()
			if tc.options.State == "resolved" {
				want.Items[0].State = "resolved"
				want.Items[0].Reason = "lease_released"
			}
			s.LeaseAttention = func(ctx context.Context, options workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if options != tc.options || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
					t.Fatal("misbound or unbounded inspection", options)
				}
				return want, nil
			}
			h, _ := New(token, 1, s)
			h.slots <- struct{}{}
			for range 2 {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention"+tc.query, ""))
				var got workers.LeaseAttentionPage
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) {
					t.Fatal(w.Code, w.Body.String())
				}
				if w.Header().Get("Cache-Control") != "no-store" || len(h.controls) != 0 || len(h.slots) != 1 {
					t.Fatal("capacity or cache leak")
				}
			}
			if calls != 2 {
				t.Fatal(calls)
			}
		})
	}
}

func TestLeaseAttentionHTTPAdmission(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "bare_query", "body", "unknown_length", "transfer", "hidden_body", "missing_hook", "capacity", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			calls := 0
			s.LeaseAttention = func(context.Context, workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
				calls++
				return attentionPageFixture(), nil
			}
			if mode == "missing_hook" {
				s.LeaseAttention = nil
			}
			h, _ := New(token, 1, s)
			r := continuationRequest("GET", "/v1/resources/attention", "")
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
			case "bare_query":
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

func TestLeaseAttentionHTTPRejectsMalformedQueries(t *testing.T) {
	s := services()
	s.LeaseAttention = func(context.Context, workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
		t.Fatal("malformed query reached backend")
		return workers.LeaseAttentionPage{}, nil
	}
	h, _ := New(token, 1, s)
	for _, query := range []string{"&", "state=open&", "&state=open", "state=open&&limit=1", "state=", "after=", "limit=", "unknown=x", "state=unknown", "state=open&state=open", "after=a&after=b", "limit=1&limit=1", "limit=01", "limit=+1", "limit=-1", "limit=0", "limit=101", "limit=1.0", "limit=999999999999999999999999", "after=bad+cursor", "after=%00", "after=%FF", "state=%ZZ", "state=open;limit=1", "state=open&%73tate=open", "after=" + strings.Repeat("x", 129), "unknown=" + strings.Repeat("x", 2049)} {
		t.Run(query[:min(len(query), 80)], func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention?"+query, ""))
			if w.Code != 400 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestLeaseAttentionHTTPMethods(t *testing.T) {
	s := services()
	s.LeaseAttention = func(context.Context, workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
		t.Fatal("method reached backend")
		return workers.LeaseAttentionPage{}, nil
	}
	h, _ := New(token, 1, s)
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH", "HEAD"} {
		for _, query := range []string{"", "?state=all"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, continuationRequest(method, "/v1/resources/attention"+query, ""))
			if w.Code != 405 || w.Header().Get("Allow") != "GET" {
				t.Fatal(method, w.Code, w.Body.String())
			}
		}
	}
}

func TestLeaseAttentionHTTPBackendValidation(t *testing.T) {
	for _, mode := range []string{"error", "panic", "canceled", "version", "schema", "unavailable", "nil_items", "item_version", "private_reason", "time", "wrong_filter", "old_cursor", "too_many", "short_more", "wrong_next", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			query := "?state=open&after=attention-a&limit=1"
			if mode == "short_more" {
				query = "?limit=2"
			}
			s.LeaseAttention = func(context.Context, workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
				out := attentionPageFixture()
				switch mode {
				case "error":
					return out, errors.New("private backend secret")
				case "panic":
					panic("private backend secret")
				case "canceled":
					cancel()
				case "version":
					out.Version = 2
				case "schema":
					out.StorageSchema = 31
				case "unavailable":
					out.Available = false
				case "nil_items":
					out.Items = nil
				case "item_version":
					out.Items[0].Version = 2
				case "private_reason":
					out.Items[0].Reason = "private backend secret"
				case "time":
					out.Items[0].UpdatedAt = time.Time{}
				case "wrong_filter":
					out.Items[0].State = "resolved"
					out.Items[0].Reason = "lease_released"
				case "old_cursor":
					out.Items[0].ID = "attention-a"
				case "too_many":
					item := out.Items[0]
					item.ID = "attention-c"
					out.Items = append(out.Items, item)
				case "short_more":
					out.HasMore = true
					out.NextCursor = out.Items[0].ID
				case "wrong_next":
					out.HasMore = true
					out.NextCursor = "other-id"
				case "duplicate":
					out.Items = append(out.Items, out.Items[0])
				}
				return out, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention"+query, "").WithContext(ctx))
			want := 500
			if mode == "error" || mode == "panic" || mode == "canceled" {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "task_id") || len(h.controls) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestLeaseAttentionHTTPLegacyUnavailableAndCursor(t *testing.T) {
	for _, schema := range []int{1, 23, 24} {
		s := services()
		want := workers.LeaseAttentionPage{Version: 1, StorageSchema: schema, Available: schema >= 24, Items: []workers.LeaseAttention{}}
		if schema == 24 {
			want = attentionPageFixture()
			want.HasMore = true
			want.NextCursor = want.Items[0].ID
		}
		s.LeaseAttention = func(context.Context, workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
			return want, nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention?limit=1", ""))
		var got workers.LeaseAttentionPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) {
			t.Fatal(schema, w.Code, w.Body.String())
		}
	}
}
