package webuiapp

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type browserReviewFake struct {
	evaluations, reads int
	failed             bool
}

func (f *browserReviewFake) Evaluate(context.Context, string, remote.AutomaticRequest) (remote.RemoteEvaluationStatus, error) {
	f.evaluations++
	if f.failed {
		return remote.RemoteEvaluationStatus{}, errors.New("secret diagnostic")
	}
	return remote.RemoteEvaluationStatus{Version: 1, Status: "completed", ReceiptSHA256: strings.Repeat("a", 64), ReviewApplied: true, Review: &remote.OutcomeReview{Review: harness.Review{Reviewer: "private-reviewer-metadata", Verdict: "failed"}}}, nil
}
func (f *browserReviewFake) InspectReview(context.Context, string, remote.AutomaticRequest) (remote.AutomaticReviewState, error) {
	f.reads++
	return remote.AutomaticReviewState{Version: 1, ReceiptSHA256: strings.Repeat("a", 64), Recorded: true, Evidence: &harness.ReviewState{Classification: "advisory", Head: &harness.Review{Method: "automated_ai", Verdict: "passed", Reviewer: "private-reviewer-metadata"}}}, nil
}
func TestBrowserAutomaticReviewAuthorityAndCurrentHead(t *testing.T) {
	h := mutationHandlerFixture(t, MutationServices{})
	f := &browserReviewFake{}
	h.remoteReviewer = f
	body := `{"action":"evaluate","request":{"version":1,"request_id":"automatic-review-001","prompt":"original instructions","domain":"coding","profile":"default","difficulty":"hard","context_tokens":32768,"private":true,"max_cost":0,"capabilities":[]}}`
	call := func(r *http.Request, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%d want %d: %s", w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	request := func(body string) *http.Request {
		return authorizedMutationRequest(t, h, "/app/api/v1/remote-auto-review", body)
	}
	call(browserRequest(http.MethodPost, "/app/api/v1/remote-auto-review", body), 401)
	r := request(body)
	r.Header.Del("X-Darwin-CSRF")
	call(r, 403)
	call(request(strings.Replace(body, `"action":"evaluate"`, `"action":"evaluate","verdict":"passed"`, 1)), 400)
	call(request(strings.Replace(body, `"hard"`, `"invented"`, 1)), 400)
	if f.evaluations != 0 {
		t.Fatal("unauthorized review")
	}
	out := call(request(body), 200)
	if !strings.Contains(out, `"review_applied":true`) || strings.Contains(out, "private-reviewer") || strings.Contains(out, "failed") {
		t.Fatal("old evaluation head projected", out)
	}
	status := strings.Replace(body, `"evaluate"`, `"status"`, 1)
	out = call(request(status), 200)
	if f.evaluations != 1 || f.reads != 1 || !strings.Contains(out, `"method":"automated_ai"`) || !strings.Contains(out, `"classification":"advisory"`) || !strings.Contains(out, `"verdict":"passed"`) || strings.Contains(out, "private-reviewer") {
		t.Fatal("current head lost", out)
	}
	f.failed = true
	out = call(request(body), 503)
	if f.evaluations != 2 || strings.Contains(out, "secret") || !strings.Contains(out, "review_unconfirmed") {
		t.Fatal("review retried or leaked", out)
	}
	h.remoteReviewer = nil
	call(request(body), 503)
	if f.evaluations != 2 {
		t.Fatal("disabled reviewer invoked")
	}
}
func TestBrowserReviewerRejectsMissingOriginalIntentBeforePolicy(t *testing.T) {
	store, err := remote.OpenRouteStore(filepath.Join(t.TempDir(), "routes"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	d := &RecordedRemoteReviewer{Client: &remote.Client{}, Store: store, Policy: func(bool) (remote.RemoteEvaluator, error) { calls++; return remote.RemoteEvaluator{}, nil }}
	if _, err = d.Evaluate(context.Background(), "unknown-request-001", remote.AutomaticRequest{Version: 1}); err == nil || calls != 0 {
		t.Fatal("missing binding reached evaluator policy", err)
	}
}
