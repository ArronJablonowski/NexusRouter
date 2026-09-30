package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

func taskLeaseFixture() workers.TaskLeaseStatus {
	return workers.TaskLeaseStatus{Version: 1, TaskID: "task", TaskState: "failed", Sequence: 2, ObservedAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), StorageSchema: 23,
		Leases:     &workers.TaskLeaseCounts{LiveReaders: 1, ExpiredReaders: 2, ReleasedReaders: 3, LiveWriters: 1, ExpiredWriters: 2, ReleasedWriters: 3},
		Recoveries: &workers.TaskRecoveryCounts{TerminalReaders: 1, OrphanWorkers: 1, InterruptedChildren: 1}}
}

type leaseUnreadBody struct{ t *testing.T }

func (b leaseUnreadBody) Read([]byte) (int, error) {
	b.t.Fatal("inspection read request content")
	return 0, errors.New("read forbidden")
}
func (b leaseUnreadBody) Close() error { return nil }

func TestTaskLeasesMetadataOnly(t *testing.T) {
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("inspection executed task")
		return app.Result{}, nil
	}
	s.Inspect = func(context.Context, string) (sessions.Snapshot, error) {
		t.Fatal("inspection loaded history")
		return sessions.Snapshot{}, nil
	}
	calls := 0
	s.TaskLeases = func(ctx context.Context, task string) (workers.TaskLeaseStatus, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if task != "task" || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
			t.Fatal("unbounded or misbound observation")
		}
		return taskLeaseFixture(), nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	h.slots <- struct{}{} // Observations must remain available while execution is full.
	for range 2 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/tasks/task/leases", ""))
		var got workers.TaskLeaseStatus
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, taskLeaseFixture()) {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || len(h.controls) != 0 || len(h.slots) != 1 {
			t.Fatal("diagnostic caching/capacity")
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestTaskLeasesAdmission(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "query", "bare_query", "body", "unknown_length", "transfer", "hidden_body", "method", "invalid_id", "missing_hook", "capacity", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			calls := 0
			s.TaskLeases = func(context.Context, string) (workers.TaskLeaseStatus, error) {
				calls++
				return taskLeaseFixture(), nil
			}
			if mode == "missing_hook" {
				s.TaskLeases = nil
			}
			h, _ := New(token, 1, s)
			r := continuationRequest("GET", "/v1/tasks/task/leases", "")
			want := 400
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "query":
				r.URL.RawQuery = "private=value"
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
			case "method":
				r.Method = "POST"
				want = 405
			case "invalid_id":
				r.URL.Path = "/v1/tasks/task/extra/leases"
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

func TestTaskLeasesBackendValidationAndErrors(t *testing.T) {
	for _, mode := range []string{"missing", "error", "panic", "version", "task", "state", "sequence", "terminal_start", "running_recovered", "completed_orphan", "time", "schema", "negative_lease", "negative_recovery", "overflow", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.TaskLeases = func(context.Context, string) (workers.TaskLeaseStatus, error) {
				out := taskLeaseFixture()
				switch mode {
				case "missing":
					return out, sql.ErrNoRows
				case "error":
					return out, errors.New("private backend payload")
				case "panic":
					panic("private backend payload")
				case "version":
					out.Version = 2
				case "task":
					out.TaskID = "another-task"
				case "state":
					out.TaskState = "private backend payload"
				case "sequence":
					out.Sequence = -1
				case "terminal_start":
					out.Sequence = 1
				case "running_recovered":
					out.TaskState = "running"
				case "completed_orphan":
					out.TaskState = "completed"
				case "time":
					out.ObservedAt = time.Time{}
				case "schema":
					out.StorageSchema = 999
				case "negative_lease":
					out.Leases.LiveReaders = -1
				case "negative_recovery":
					out.Recoveries.TerminalReaders = -1
				case "overflow":
					out.Leases.LiveReaders = 1<<63 - 1
				case "canceled":
					cancel()
				}
				return out, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, continuationRequest("GET", "/v1/tasks/task/leases", "").WithContext(ctx))
			want := 500
			if mode == "missing" {
				want = 404
			}
			if mode == "error" || mode == "panic" || mode == "canceled" {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "task_id") || len(h.controls) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestTaskLeasesReadOnlyMethods(t *testing.T) {
	s := services()
	s.TaskLeases = func(context.Context, string) (workers.TaskLeaseStatus, error) {
		t.Fatal("method reached backend")
		return workers.TaskLeaseStatus{}, nil
	}
	h, _ := New(token, 1, s)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest(method, "/v1/tasks/task/leases", ""))
		if w.Code != 405 || w.Header().Get("Allow") != "GET" {
			t.Fatal(method, w.Code)
		}
	}
}

func TestTaskLeasesLegacyAvailability(t *testing.T) {
	for _, version := range []int{1, 2, 3, 22, 23} {
		s := services()
		s.TaskLeases = func(context.Context, string) (workers.TaskLeaseStatus, error) {
			out := taskLeaseFixture()
			out.StorageSchema = version
			if version < 3 {
				out.Leases = nil
			}
			if version < 23 {
				out.Recoveries = nil
			}
			return out, nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest("GET", "/v1/tasks/task/leases", ""))
		var status workers.TaskLeaseStatus
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || (status.Leases == nil) != (version < 3) || (status.Recoveries == nil) != (version < 23) {
			t.Fatal(version, w.Code, w.Body.String())
		}
		if version < 3 && !strings.Contains(w.Body.String(), `"leases":null`) {
			t.Fatal("unsupported leases presented as verified zero")
		}
		if version < 23 && !strings.Contains(w.Body.String(), `"recoveries":null`) {
			t.Fatal("unsupported recoveries presented as verified zero")
		}
	}
}
