package webuiapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type browserOutcomeJournal struct{ events []runtime.Event }

func (j *browserOutcomeJournal) Append(_ context.Context, seq int64, e runtime.Event) error {
	if seq != int64(len(j.events)) || e.Sequence != seq+1 || e.Validate() != nil {
		return remote.ErrInvalid
	}
	copy, err := e.Clone()
	if err != nil {
		return err
	}
	j.events = append(j.events, copy)
	return nil
}

type browserContentEvaluator struct {
	calls atomic.Int32
	fail  bool
	t     *testing.T
}

func (*browserContentEvaluator) Descriptor() evaluation.EvaluatorDescriptor {
	return evaluation.EvaluatorDescriptor{Version: 1, ID: "browser-review-fixture", Revision: "1", RubricVersion: "fixture-v1"}
}
func (e *browserContentEvaluator) Evaluate(_ context.Context, input evaluation.EvaluatorRequest) (evaluation.EvaluatorResponse, error) {
	e.calls.Add(1)
	if input.Requirements != "private fixture task" || input.Candidate != "fixture answer" || input.Domain != "coding" || len(input.Evidence) != 1 {
		e.t.Error("review did not receive authenticated requirements/output", input)
	}
	if e.fail {
		return evaluation.EvaluatorResponse{}, errors.New("private fixture evaluator diagnostic")
	}
	return evaluation.EvaluatorResponse{Version: 1, Audit: evaluation.Audit{Version: 1, EvaluatorID: e.Descriptor().ID, RubricVersion: e.Descriptor().RubricVersion, Domain: input.Domain, Verdict: "accept", Confidence: 1, Findings: []evaluation.AuditFinding{{Summary: "synthetic fixture review", EvidenceRefs: []string{"requirements", "candidate", "remote_execution"}}}}}, nil
}

// Called from the production discovery/dispatch fixture after lost-response
// recovery. Runtime events and the evaluator are synthetic; HTTP, mTLS,
// canonical validation, durable evaluator attempts and scoped learning are real.
func verifyBrowserRemoteReview(t *testing.T, h *Handler, backend *browserRemoteExecution, payload string) {
	t.Helper()
	ctx := context.Background()
	cookie, csrf := authenticateBrowser(t, h)
	auto := h.remoteAutomatic.(*RecordedRemoteAutomatic)
	var input remoteAutomaticRequest
	if err := json.Unmarshal([]byte(payload), &input); err != nil {
		t.Fatal(err)
	}
	request, err := input.request()
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := auto.Store.ResolveAutomatic(input.RequestID, request)
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	var submission string
	for key := range backend.tasks {
		submission = key
	}
	backend.mu.Unlock()
	journal := &browserOutcomeJournal{}
	_, output, err := runtime.RunHarness(ctx, journal, runtime.HarnessRequest{
		TaskID: "review-task", SessionID: "review-session", SubmissionID: submission,
		Messages: []providers.Message{{Role: "user", Content: task.Prompt}}, Privacy: "local_only",
		ContextTokens: task.ContextTokens, MaxOutputBytes: 4096,
		Attribution: runtime.HarnessAttribution{Identity: backend.identity, Task: request.Routing.Task},
		Execute: func(context.Context) (runtime.HarnessOutput, error) {
			return runtime.HarnessOutput{Actual: backend.identity, Text: "fixture answer"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.events = journal.events
	backend.tasks[submission] = submissions.Status{Version: 1, ID: submission, State: "succeeded", TaskIDs: []string{"review-task"}, Result: &submissions.Result{TaskID: "review-task", Text: output}}
	backend.mu.Unlock()
	for _, failed := range []bool{false, true} {
		name := "accepted"
		if failed {
			name = "failed"
		}
		t.Run("review_"+name, func(t *testing.T) {
			// Separate evidence roots exercise two evaluator outcomes for one fixture
			// without re-dispatching inference or changing any canonical events.
			root := filepath.Join(t.TempDir(), "evidence")
			evaluator := &browserContentEvaluator{t: t, fail: failed}
			var policies atomic.Int32
			reviewer := &RecordedRemoteReviewer{Client: auto.Client, Store: auto.Store, EvidenceRoot: root, Policy: func(private bool) (remote.RemoteEvaluator, error) {
				policies.Add(1)
				if !private {
					t.Error("private requirement was lost")
				}
				return remote.RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}, nil
			}}
			h.remoteReviewer = reviewer
			call := func(action, original string, want int) remoteReviewPage {
				t.Helper()
				body := `{"action":"` + action + `","request":` + original + `}`
				w := httptest.NewRecorder()
				r := browserRequest(http.MethodPost, "/app/api/v1/remote-auto-review", body)
				r.AddCookie(cookie)
				r.Header.Set("X-Darwin-CSRF", csrf)
				h.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("%s: %d want %d: %s", action, w.Code, want, w.Body.String())
				}
				for _, secret := range []string{"private fixture task", "fixture answer", "private fixture evaluator diagnostic"} {
					if strings.Contains(w.Body.String(), secret) {
						t.Fatal("raw content exposed in review metadata")
					}
				}
				var page remoteReviewPage
				if want == 200 {
					if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
						t.Fatal(err)
					}
				}
				return page
			}
			if p := call("status", payload, 200); p.Status != "unrecorded" || policies.Load() != 0 || evaluator.calls.Load() != 0 {
				t.Fatal(p)
			}
			call("evaluate", strings.Replace(payload, "private fixture task", "altered prompt", 1), 503)
			if policies.Load() != 0 || evaluator.calls.Load() != 0 {
				t.Fatal("changed intent reached evaluator policy")
			}
			want := 200
			if failed {
				want = 503
			}
			// Discard the first response: subsequent identical requests must only read
			// the durable terminal attempt, including failure, never invoke again.
			call("evaluate", payload, want)
			call("evaluate", payload, want)
			p := call("status", payload, 200)
			if evaluator.calls.Load() != 1 {
				t.Fatal("evaluation replay", evaluator.calls.Load())
			}
			if failed {
				if p.Verdict != "" || p.Classification == "advisory" || p.Classification == "confirmed" {
					t.Fatal("failed evaluation gained quality", p)
				}
			} else if p.Verdict != "passed" || p.Method != "automated_ai" || p.Classification != "advisory" {
				t.Fatal(p)
			}
			rank := func() harness.Ranked {
				t.Helper()
				candidate := remote.DestinationCandidate{Destination: "node-a", ModelID: task.ModelID, HarnessID: task.HarnessID, Candidate: harness.Candidate{Identity: backend.identity, Local: true, Available: true, Authorized: true, Compatible: true, CredentialAvailable: true, CapacityAvailable: true, ContextTokens: 32768}}
				result, err := auto.Client.RankRecordedCandidates(ctx, root, request.Routing, harness.DefaultPolicy(), []remote.DestinationCandidate{candidate}, 0)
				if err != nil {
					t.Fatal(err)
				}
				return result.Selection.Primary.Ranked
			}
			ranked := rank()
			expectedAdvisory := 1
			if failed {
				expectedAdvisory = 0
			}
			if ranked.AdvisorySamples != expectedAdvisory || ranked.ConfirmedSamples != 0 {
				t.Fatal("wrong learned weight", ranked)
			}
			if !failed {
				current, err := reviewer.InspectReview(ctx, input.RequestID, request)
				if err != nil || current.Evidence == nil || current.Evidence.Head == nil {
					t.Fatal(current, err)
				}
				revised := *current.Evidence.Head
				revised.ID = "fixture-corrected-head"
				revised.ExpectedHead = current.Evidence.Head.ID
				revised.Method = "deterministic"
				revised.Verdict = "failed"
				revised.Quality = 0
				revised.CreatedAt = time.Now().UTC()
				if err = auto.Client.ReviewRecordedOutcome(ctx, auto.Store, root, input.RequestID, task, remote.OutcomeReview{Version: 1, ReceiptSHA256: current.ReceiptSHA256, Review: revised}); err != nil {
					t.Fatal(err)
				}
				call("evaluate", payload, 200)
				p = call("status", payload, 200)
				ranked = rank()
				if p.Verdict != "failed" || p.Method != "deterministic" || p.Classification != "confirmed" || ranked.AdvisorySamples != 0 || ranked.ConfirmedSamples != 1 || evaluator.calls.Load() != 1 {
					t.Fatal("stale evaluation replaced current head", p, ranked)
				}
			}
			if backend.creates.Load() != 1 || backend.catalogues.Load() != 1 {
				t.Fatal("review redispatched or rediscovered")
			}
		})
	}
}
