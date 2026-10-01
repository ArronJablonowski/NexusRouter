package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/harness"
)

type remoteEvaluatorFixture struct {
	calls    atomic.Int32
	revision string
	run      func(context.Context, evaluation.EvaluatorRequest) error
	verdict  string
}

func (e *remoteEvaluatorFixture) Descriptor() evaluation.EvaluatorDescriptor {
	revision := e.revision
	if revision == "" {
		revision = "fixture-v1"
	}
	return evaluation.EvaluatorDescriptor{Version: 1, ID: "trusted-reviewer", Revision: revision, RubricVersion: "fixture-rubric"}
}
func (e *remoteEvaluatorFixture) Evaluate(ctx context.Context, r evaluation.EvaluatorRequest) (evaluation.EvaluatorResponse, error) {
	e.calls.Add(1)
	if e.run != nil {
		if err := e.run(ctx, r); err != nil {
			return evaluation.EvaluatorResponse{}, err
		}
	}
	verdict := e.verdict
	if verdict == "" {
		verdict = "accept"
	}
	return evaluation.EvaluatorResponse{Version: 1, Audit: evaluation.Audit{Version: 1, EvaluatorID: e.Descriptor().ID, RubricVersion: e.Descriptor().RubricVersion, Domain: r.Domain, Verdict: verdict, Confidence: 1, Findings: []evaluation.AuditFinding{{Summary: "fixture review", EvidenceRefs: []string{"requirements", "candidate", "remote_execution"}}}}}, nil
}
func evaluationDirectory(root string, v VerifiedOutcome) string {
	r := v.Receipt()
	digest, _ := r.Digest()
	scope := hash(struct{ Destination, Caller string }{r.Route.Destination, r.Route.CallerFingerprint})
	return filepath.Join(root, scope, "evaluations", digest)
}
func TestRemoteEvaluatorCompletesOnceWithAdvisoryLearning(t *testing.T) {
	f, routes, key, task, v, _, b := reviewFixture(t)
	root := filepath.Join(t.TempDir(), "evidence")
	evaluator := &remoteEvaluatorFixture{run: func(_ context.Context, input evaluation.EvaluatorRequest) error {
		if input.Requirements != task.Prompt || input.Candidate != v.Output() || input.Domain != task.Domain || len(input.Evidence) != 1 {
			t.Error("wrong review input", input)
		}
		return nil
	}}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	var first RemoteEvaluationStatus
	for i := 0; i < 2; i++ {
		result, err := f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy)
		if err != nil || result.Status != "completed" || !result.ReviewApplied || result.Review == nil || result.Review.Review.Method != "automated_ai" {
			t.Fatal(result, err)
		}
		if i == 0 {
			first = result
		} else if *result.Review != *first.Review {
			t.Fatal("review identity changed")
		}
	}
	ranked := remoteRank(t, root, v)
	if evaluator.calls.Load() != 1 || b.submits.Load() != 1 || ranked.AdvisorySamples != 1 || ranked.ConfirmedSamples != 0 {
		t.Fatal(evaluator.calls.Load(), b.submits.Load(), ranked)
	}
	other := &remoteEvaluatorFixture{revision: "different"}
	policy.Evaluator = other
	if _, err := f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy); err != ErrConflict || other.calls.Load() != 0 {
		t.Fatal("changed policy replay", err)
	}
}
func TestRemoteEvaluatorFailureAndAbstentionNeverPass(t *testing.T) {
	for _, mode := range []string{"failed", "panic", "abstain", "reject"} {
		t.Run(mode, func(t *testing.T) {
			f, routes, key, task, v, _, _ := reviewFixture(t)
			root := filepath.Join(t.TempDir(), "evidence")
			e := &remoteEvaluatorFixture{}
			switch mode {
			case "failed":
				e.run = func(context.Context, evaluation.EvaluatorRequest) error { return errors.New("private raw error") }
			case "panic":
				e.run = func(context.Context, evaluation.EvaluatorRequest) error { panic("private raw error") }
			case "abstain":
				e.verdict = "abstain"
			case "reject":
				e.verdict = "reject"
			}
			policy := RemoteEvaluator{Evaluator: e, Local: true, Timeout: time.Second}
			for i := 0; i < 2; i++ {
				result, err := f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy)
				if mode == "failed" || mode == "panic" {
					if err == nil || result.Status != "failed" || result.Review != nil {
						t.Fatal(result, err)
					}
				} else {
					if err != nil || result.Review.Review.Verdict == "passed" {
						t.Fatal(result, err)
					}
				}
			}
			rank := remoteRank(t, root, v)
			if e.calls.Load() != 1 || rank.ConfirmedSamples != 0 {
				t.Fatal(e.calls.Load(), rank)
			}
			if mode != "reject" && rank.AdvisorySamples != 0 {
				t.Fatal("failure gained quality", rank)
			}
		})
	}
}
func TestRemoteEvaluatorConcurrentAndMissingResultNeverReinvokes(t *testing.T) {
	f, routes, key, task, v, _, _ := reviewFixture(t)
	root := filepath.Join(t.TempDir(), "evidence")
	entered, release := make(chan struct{}), make(chan struct{})
	evaluator := &remoteEvaluatorFixture{run: func(context.Context, evaluation.EvaluatorRequest) error { close(entered); <-release; return nil }}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Minute}
	done := make(chan error, 1)
	go func() {
		_, err := f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy)
		done <- err
	}()
	<-entered
	result, err := f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy)
	if err != ErrUnavailable || result.Status != "started" || evaluator.calls.Load() != 1 {
		close(release)
		<-done
		t.Fatal(result, err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	// Simulate lost terminal persistence; a durable start must never be replayed.
	if err = os.Remove(filepath.Join(evaluationDirectory(root, v), "result.json")); err != nil {
		t.Fatal(err)
	}
	result, err = f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy)
	if err != ErrUnavailable || result.Status != "started" || evaluator.calls.Load() != 1 {
		t.Fatal(result, err)
	}
}
func TestRemoteEvaluatorPrivacyAndOperatorHeadBlockInvocation(t *testing.T) {
	for _, mode := range []string{"privacy", "head", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			f, routes, key, task, _, review, _ := reviewFixture(t)
			root := filepath.Join(t.TempDir(), "evidence")
			e := &remoteEvaluatorFixture{}
			policy := RemoteEvaluator{Evaluator: e, Local: true, Timeout: time.Second}
			switch mode {
			case "privacy":
				task.Private = true
				policy.Local = false
			case "head":
				if err := f.client.ReviewRecordedOutcome(context.Background(), routes, root, key, task, review); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				writeRegistry(t, f.clientTrust)
			}
			if _, err := f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy); err == nil || e.calls.Load() != 0 {
				t.Fatal("unauthorized evaluator call", err, e.calls.Load())
			}
		})
	}
}

func TestRemoteEvaluatorReconcilesSavedResultAfterRevocation(t *testing.T) {
	f, routes, key, task, v, _, b := reviewFixture(t)
	root := filepath.Join(t.TempDir(), "evidence")
	evaluator := &remoteEvaluatorFixture{run: func(context.Context, evaluation.EvaluatorRequest) error { writeRegistry(t, f.clientTrust); return nil }}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	result, err := f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy)
	if err == nil || result.Status != "completed" || result.ReviewApplied || result.Review == nil {
		t.Fatal(result, err)
	}
	rank := remoteRank(t, root, v)
	if rank.AdvisorySamples != 0 || rank.PendingOutputs != 1 {
		t.Fatal(rank)
	}
	writeRegistry(t, f.clientTrust, f.serverPeer)
	result, err = f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy)
	if err != nil || !result.ReviewApplied || evaluator.calls.Load() != 1 || b.submits.Load() != 1 {
		t.Fatal(result, err, evaluator.calls.Load())
	}
}

func TestRemoteEvaluatorDoesNotOverwriteConcurrentOperatorReview(t *testing.T) {
	f, routes, key, task, v, review, _ := reviewFixture(t)
	root := filepath.Join(t.TempDir(), "evidence")
	review.Review.Method = "deterministic"
	review.Review.Verdict = "failed"
	review.Review.Quality = 0
	evaluator := &remoteEvaluatorFixture{run: func(context.Context, evaluation.EvaluatorRequest) error {
		return f.client.ReviewRecordedOutcome(context.Background(), routes, root, key, task, review)
	}}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	for i := 0; i < 2; i++ {
		result, err := f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy)
		if err == nil || result.Status != "completed" || result.ReviewApplied {
			t.Fatal(result, err)
		}
	}
	rank := remoteRank(t, root, v)
	if evaluator.calls.Load() != 1 || rank.AdvisorySamples != 0 || rank.ConfirmedSamples != 1 {
		t.Fatal(evaluator.calls.Load(), rank)
	}
}

func TestRemoteEvaluatorAutomaticUsesSavedChoiceWithoutRedispatch(t *testing.T) {
	f, routes, _, task, _, _, backend := reviewFixture(t)
	f.server.backend = rankingBackend{backend, *task.ExpectedHarnessIdentity}
	request := AutomaticRequest{Version: 1, Prompt: task.Prompt, Routing: harness.Request{Version: 1, Task: harness.TaskClass{Domain: task.Domain, Profile: task.Profile, Difficulty: task.HarnessDifficulty}, Mode: "local_only", LocalRequired: true, ContextTokens: int64(task.ContextTokens)}}
	key := "automatic-review-01"
	root := filepath.Join(t.TempDir(), "evidence")
	candidate := DestinationCandidate{Destination: "node-a", ModelID: task.ModelID, HarnessID: task.HarnessID, Candidate: harness.Candidate{Identity: *task.ExpectedHarnessIdentity, Local: true, Available: true, Authorized: true, Compatible: true, CredentialAvailable: true, CapacityAvailable: true, ContextTokens: 8192}}
	if _, _, err := f.client.DispatchAutomatic(context.Background(), routes, root, key, request, harness.DefaultPolicy(), []DestinationCandidate{candidate}, 0); err != nil {
		t.Fatal(err)
	}
	before := backend.submits.Load()
	evaluator := &remoteEvaluatorFixture{}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	for i := 0; i < 2; i++ {
		result, err := f.client.EvaluateAutomaticOutcome(context.Background(), routes, root, key, request, policy)
		if err != nil || !result.ReviewApplied {
			t.Fatal(result, err)
		}
	}
	request.Prompt = "changed"
	if _, err := f.client.EvaluateAutomaticOutcome(context.Background(), routes, root, key, request, policy); err != ErrConflict {
		t.Fatal(err)
	}
	if evaluator.calls.Load() != 1 || backend.submits.Load() != before {
		t.Fatal("evaluation redispatched", evaluator.calls.Load(), backend.submits.Load())
	}
}
