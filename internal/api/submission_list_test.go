package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func submissionListItem(t *testing.T, state string) submissions.Summary {
	t.Helper()
	body, err := json.Marshal(submissionFixtureStatus(state))
	if err != nil {
		t.Fatal(err)
	}
	var item submissions.Summary
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestSubmissionListingReturnsOnlyMetadataAndNeverExecutes(t *testing.T) {
	for _, state := range []string{"", "queued", "running", "succeeded", "failed", "canceled"} {
		actualState := state
		if state == "" {
			actualState = "queued"
		}
		page := submissions.Page{Version: 1, Items: []submissions.Summary{submissionListItem(t, actualState)}}
		s := services()
		s.Run = func(context.Context, app.Request) (app.Result, error) {
			t.Error("listing executed task")
			return app.Result{}, nil
		}
		s.RunStream = func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
			t.Error("listing streamed task")
			return app.Result{}, nil
		}
		s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
			t.Error("listing submitted work")
			return submissions.Status{}, nil
		}
		s.CancelSubmission = func(context.Context, string) (submissions.Status, error) {
			t.Error("listing canceled work")
			return submissions.Status{}, nil
		}
		calls := 0
		s.Submissions = func(_ context.Context, opts submissions.ListOptions) (submissions.Page, error) {
			calls++
			wantLimit := 25
			if state != "" {
				wantLimit = 1
			}
			if opts.State != state || opts.After != "" || opts.Limit != wantLimit {
				t.Error("query changed", opts)
			}
			return page, nil
		}
		h, err := New(token, 1, s)
		if err != nil {
			t.Fatal(err)
		}
		path := "/v1/submissions"
		if state != "" {
			path += "?state=" + state + "&limit=1"
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", path, ""))
		var got submissions.Page
		if w.Code != 200 || calls != 1 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, page) {
			t.Fatal(state, w.Code, calls, w.Body.String())
		}
		var raw map[string]json.RawMessage
		json.Unmarshal(w.Body.Bytes(), &raw)
		var items []map[string]json.RawMessage
		json.Unmarshal(raw["items"], &items)
		for _, item := range items {
			for _, key := range []string{"result", "request", "token", "idempotency_key", "key"} {
				if item[key] != nil {
					t.Fatal("private or result field in listing", key)
				}
			}
		}
		if strings.Contains(w.Body.String(), `"answer"`) || strings.Contains(w.Body.String(), token) {
			t.Fatal("result or token leaked in metadata", w.Body.String())
		}
	}
}

func TestSubmissionListingStrictQuery(t *testing.T) {
	for _, query := range []string{
		"unknown=x", "State=queued", "state=unknown", "state=completed", "state=queued&state=queued", "after=&after=", "limit=1&limit=2", "%6cimit=1&limit=2",
		"limit=", "limit=0", "limit=101", "limit=-1", "limit=01", "limit=+1", "limit=1.0", "limit=1e0", "limit=9223372036854775808",
		"after=not-a-canonical-cursor", "after=" + strings.Repeat("a", 1025), "after=%ZZ", "limit=%", "state=queued;limit=1",
	} {
		s := services()
		s.Submissions = func(context.Context, submissions.ListOptions) (submissions.Page, error) {
			t.Error("invalid query reached listing hook", query)
			return submissions.Page{}, nil
		}
		h, _ := New(token, 1, s)
		r := request("GET", "/v1/submissions", "")
		r.URL.RawQuery = query
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"limit=1", "limit=100", "state=&after=&limit=25"} {
		s := services()
		s.Submissions = func(context.Context, submissions.ListOptions) (submissions.Page, error) {
			return submissions.Page{Version: 1, Items: []submissions.Summary{}}, nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions?"+query, ""))
		if w.Code != 200 {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
}

func TestSubmissionListingQueryExceptionIsExactAndAuthenticated(t *testing.T) {
	for _, condition := range []string{"auth", "origin", "post", "detail", "trailing-slash", "other-path", "missing-hook"} {
		s := services()
		calls := 0
		s.Submissions = func(context.Context, submissions.ListOptions) (submissions.Page, error) {
			calls++
			return submissions.Page{Version: 1}, nil
		}
		if condition == "missing-hook" {
			s.Submissions = nil
		}
		h, _ := New(token, 1, s)
		r := request("GET", "/v1/submissions?limit=1", "")
		want := 400
		switch condition {
		case "auth":
			r.Header.Del("Authorization")
			want = 401
		case "origin":
			r.Header.Set("Origin", "https://example.com")
			want = 403
		case "post":
			r.Method = "POST"
		case "detail":
			r.URL.Path = "/v1/submissions/submission"
		case "trailing-slash":
			r.URL.Path = "/v1/submissions/"
		case "other-path":
			r.URL.Path = "/v1/tasks/task"
		case "missing-hook":
			want = 503
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want || calls != 0 {
			t.Fatal(condition, w.Code, calls, w.Body.String())
		}
	}
}

func TestSubmissionListingUsesOnlyControlCapacity(t *testing.T) {
	s := services()
	calls := 0
	s.Submissions = func(context.Context, submissions.ListOptions) (submissions.Page, error) {
		calls++
		return submissions.Page{Version: 1, Items: []submissions.Summary{}}, nil
	}
	h, _ := New(token, 1, s)
	h.slots <- struct{}{}
	defer func() { <-h.slots }()
	h.intake <- struct{}{}
	h.intake <- struct{}{}
	defer func() { <-h.intake; <-h.intake }()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/submissions?limit=1", ""))
	if w.Code != 200 || calls != 1 {
		t.Fatal("execution/intake capacity blocked listing", w.Code, calls)
	}
	h.controls <- struct{}{}
	h.controls <- struct{}{}
	defer func() { <-h.controls; <-h.controls }()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/submissions?limit=1", ""))
	if w.Code != 503 || calls != 1 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("control capacity ignored", w.Code, calls)
	}
}

func TestSubmissionListingErrorsAndInvalidPagesRemainGeneric(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{{sql.ErrNoRows, 404}, {submissions.ErrConflict, 409}, {submissions.ErrInvalid, 400}, {submissions.ErrCapacity, 503}, {app.ErrAdmission, 422}, {errors.New("private-store-detail"), 500}} {
		s := services()
		s.Submissions = func(context.Context, submissions.ListOptions) (submissions.Page, error) {
			return submissions.Page{}, tc.err
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions", ""))
		if w.Code != tc.code || strings.Contains(w.Body.String(), "private-store-detail") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, mutate := range []func(*submissions.Page){
		func(p *submissions.Page) { p.Version = 0 },
		func(p *submissions.Page) { p.Items[0].ID = "private invalid id" },
		func(p *submissions.Page) { p.Items[0].ConfigDigest = "private-digest" },
		func(p *submissions.Page) { p.Items[0].ErrorCode = "private-error" },
		func(p *submissions.Page) { p.Items[0].State = "unknown" },
		func(p *submissions.Page) { p.HasMore = true },
		func(p *submissions.Page) { p.NextCursor = "private-cursor" },
		func(p *submissions.Page) { p.Items = append(p.Items, p.Items[0]) },
	} {
		page := submissions.Page{Version: 1, Items: []submissions.Summary{submissionListItem(t, "queued")}}
		mutate(&page)
		s := services()
		s.Submissions = func(context.Context, submissions.ListOptions) (submissions.Page, error) { return page, nil }
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions", ""))
		if w.Code != 500 || strings.Contains(w.Body.String(), "private-") {
			t.Fatal("unsafe page returned", w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"state=failed", "limit=1"} {
		page := submissions.Page{Version: 1, Items: []submissions.Summary{submissionListItem(t, "queued"), submissionListItem(t, "queued")}}
		page.Items[1].ID = "second"
		s := services()
		s.Submissions = func(context.Context, submissions.ListOptions) (submissions.Page, error) { return page, nil }
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions?"+query, ""))
		if w.Code != 500 {
			t.Fatal("query/page mismatch accepted", query, w.Code, w.Body.String())
		}
	}
}

func TestSubmissionListingCursorsAreBoundToFilterAndAdvance(t *testing.T) {
	after, err := submissions.EncodeListCursor(submissions.ListCursor{Version: 1, Last: 1, HighWater: 3, State: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := submissions.EncodeListCursor(submissions.ListCursor{Version: 1, Last: 2, HighWater: 3, State: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	for _, condition := range []string{"valid", "wrong-request-filter", "wrong-next-filter", "same-next-cursor", "changed-fence", "backwards-cursor"} {
		page := submissions.Page{Version: 1, Items: []submissions.Summary{submissionListItem(t, "queued")}, HasMore: true, NextCursor: next}
		state := "queued"
		switch condition {
		case "wrong-request-filter":
			state = "failed"
		case "wrong-next-filter":
			page.NextCursor, err = submissions.EncodeListCursor(submissions.ListCursor{Version: 1, Last: 2, HighWater: 3, State: "failed"})
			if err != nil {
				t.Fatal(err)
			}
		case "same-next-cursor":
			page.NextCursor = after
		case "changed-fence":
			page.NextCursor, err = submissions.EncodeListCursor(submissions.ListCursor{Version: 1, Last: 2, HighWater: 4, State: "queued"})
			if err != nil {
				t.Fatal(err)
			}
		case "backwards-cursor":
			page.NextCursor, err = submissions.EncodeListCursor(submissions.ListCursor{Version: 1, Last: 0, HighWater: 3, State: "queued"})
			if err != nil {
				t.Fatal(err)
			}
		}
		calls := 0
		s := services()
		s.Submissions = func(_ context.Context, opts submissions.ListOptions) (submissions.Page, error) {
			calls++
			if opts.After != after || opts.State != state || opts.Limit != 5 {
				t.Error("cursor query changed", opts)
			}
			return page, nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions?state="+state+"&limit=5&after="+url.QueryEscape(after), ""))
		want := 500
		if condition == "valid" {
			want = 200
		}
		if condition == "wrong-request-filter" {
			want = 400
		}
		if w.Code != want || condition == "wrong-request-filter" && calls != 0 {
			t.Fatal(condition, w.Code, calls, w.Body.String())
		}
		if condition == "valid" {
			var got submissions.Page
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, page) {
				t.Fatal("cursor page changed", got)
			}
		}
	}
}
