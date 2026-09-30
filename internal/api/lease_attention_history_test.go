package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/stateschema"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

func attentionHistoryFixture() workers.LeaseAttentionHistoryPage {
	return workers.LeaseAttentionHistoryPage{Version: 1, StorageSchema: 25, Available: true, AttentionID: "attention-b", Items: []workers.LeaseAttentionTransition{{Version: 1, Sequence: 1, Kind: "observed", Observation: attentionPageFixture().Items[0]}}}
}

func TestAttentionHistoryHTTPBoundedRead(t *testing.T) {
	for _, schema := range []int{1, 24, 25} {
		s := services()
		want := attentionHistoryFixture()
		want.StorageSchema = schema
		want.Available = schema >= 25
		if schema < 25 {
			want.Items = []workers.LeaseAttentionTransition{}
		}
		calls := 0
		s.LeaseAttentionHistory = func(ctx context.Context, id string, options workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if id != "attention-b" || options != (workers.LeaseAttentionHistoryOptions{Limit: 25}) || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
				t.Fatal("wrong binding or deadline")
			}
			return want, nil
		}
		h, _ := New(token, 1, s)
		h.slots <- struct{}{}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention/attention-b/history", ""))
		var got workers.LeaseAttentionHistoryPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) || calls != 1 || len(h.controls) != 0 || len(h.slots) != 1 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(schema, w.Code, w.Body.String())
		}
	}
}

func TestAttentionHistoryHTTPAdmission(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "bare", "body", "unknown_length", "hidden_body", "transfer", "missing_hook", "capacity", "canceled", "id"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			calls := 0
			s.LeaseAttentionHistory = func(context.Context, string, workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error) {
				calls++
				return attentionHistoryFixture(), nil
			}
			if mode == "missing_hook" {
				s.LeaseAttentionHistory = nil
			}
			h, _ := New(token, 1, s)
			r := continuationRequest("GET", "/v1/resources/attention/attention-b/history", "")
			want := 400
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				r.URL.RawQuery = "bad=%ZZ"
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				r.URL.RawQuery = "bad=%ZZ"
				want = 403
			case "bare":
				r.URL.ForceQuery = true
			case "body":
				r.Body = leaseUnreadBody{t}
				r.ContentLength = 1
			case "unknown_length":
				r.Body = leaseUnreadBody{t}
				r.ContentLength = -1
			case "hidden_body":
				r.Body = leaseUnreadBody{t}
			case "transfer":
				r.Body = leaseUnreadBody{t}
				r.TransferEncoding = []string{"chunked"}
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
			case "id":
				r.URL.Path = "/v1/resources/attention/extra/id/history"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestAttentionHistoryHTTPQueriesAndMethods(t *testing.T) {
	s := services()
	s.LeaseAttentionHistory = func(context.Context, string, workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error) {
		t.Fatal("invalid request reached backend")
		return workers.LeaseAttentionHistoryPage{}, nil
	}
	h, _ := New(token, 1, s)
	for _, query := range []string{"&", "limit=1&", "limit=1&&after_sequence=0", "unknown=x", "limit=", "after_sequence=", "limit=0", "limit=101", "limit=01", "limit=+1", "after_sequence=-1", "after_sequence=00", "after_sequence=+1", "after_sequence=9223372036854775707", "after_sequence=9223372036854775808", "after_sequence=0&after_sequence=0", "limit=1&limit=1", "limit=%ZZ", "limit=1;after_sequence=0", "x=" + strings.Repeat("a", 2049)} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention/attention-b/history?"+query, ""))
		if w.Code != 400 {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	for _, method := range []string{"HEAD", "POST", "PUT", "DELETE", "PATCH"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest(method, "/v1/resources/attention/attention-b/history?limit=1", ""))
		if w.Code != 405 || w.Header().Get("Allow") != "GET" {
			t.Fatal(method, w.Code)
		}
	}
}

func TestAttentionHistoryHTTPBackendBinding(t *testing.T) {
	for _, mode := range []string{"missing", "error", "panic", "canceled", "version", "schema", "id", "sequence", "item_id", "too_many", "short_more", "next", "nil", "reason"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.LeaseAttentionHistory = func(context.Context, string, workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error) {
				out := attentionHistoryFixture()
				switch mode {
				case "missing":
					return out, sql.ErrNoRows
				case "error":
					return out, errors.New("private backend content")
				case "panic":
					panic("private backend content")
				case "canceled":
					cancel()
				case "version":
					out.Version = 2
				case "schema":
					out.StorageSchema = stateschema.Current + 1
				case "id":
					out.AttentionID = "other"
					out.Items[0].Observation.ID = "other"
				case "sequence":
					out.Items[0].Sequence = 2
				case "item_id":
					out.Items[0].Observation.ID = "other"
				case "too_many":
					item := out.Items[0]
					item.Sequence = 2
					out.Items = append(out.Items, item)
				case "short_more":
					out.HasMore = true
					out.NextSequence = 1
				case "next":
					out.HasMore = true
					out.NextSequence = 2
				case "nil":
					out.Items = nil
				case "reason":
					out.Items[0].Observation.Reason = "private backend content"
				}
				return out, nil
			}
			query := "?limit=1"
			if mode == "short_more" {
				query = "?limit=2"
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention/attention-b/history"+query, "").WithContext(ctx))
			want := 500
			if mode == "missing" {
				want = 404
			}
			if mode == "error" || mode == "panic" || mode == "canceled" {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "observation") || len(h.controls) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestAttentionHistoryHTTPAfterSequence(t *testing.T) {
	s := services()
	want := attentionHistoryFixture()
	want.Items[0].Sequence = 3
	want.HasMore = true
	want.NextSequence = 3
	s.LeaseAttentionHistory = func(_ context.Context, _ string, o workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error) {
		if o.AfterSequence != 2 || o.Limit != 1 {
			t.Fatal(o)
		}
		return want, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, continuationRequest("GET", "/v1/resources/attention/attention-b/history?after_sequence=2&limit=1", ""))
	var got workers.LeaseAttentionHistoryPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(w.Code, w.Body.String())
	}
}
