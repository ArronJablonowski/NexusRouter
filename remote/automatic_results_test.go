package remote

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutomaticResultsBindIntentAndReviewWithoutDispatch(t *testing.T) {
	f, routes, _, task, _, review, b := reviewFixture(t)
	f.server.backend = rankingBackend{b, *task.ExpectedHarnessIdentity}
	request := AutomaticRequest{Version: 1, Prompt: task.Prompt, Routing: harness.Request{Version: 1, Task: harness.TaskClass{Domain: task.Domain, Profile: task.Profile, Difficulty: task.HarnessDifficulty}, Mode: "local_only", LocalRequired: true, ContextTokens: int64(task.ContextTokens)}}
	key := "automatic-result-01"
	root := filepath.Join(t.TempDir(), "evidence")
	candidate := DestinationCandidate{Destination: "node-a", ModelID: task.ModelID, HarnessID: task.HarnessID, Candidate: harness.Candidate{Identity: *task.ExpectedHarnessIdentity, Local: true, Available: true, Authorized: true, Compatible: true, CredentialAvailable: true, CapacityAvailable: true, ContextTokens: 8192}}
	_, choice, e := f.client.DispatchAutomatic(context.Background(), routes, root, key, request, harness.DefaultPolicy(), []DestinationCandidate{candidate}, 0)
	if e != nil {
		t.Fatal(e)
	}

	before := b.submits.Load()
	unrecorded, e := f.client.InspectAutomaticReview(context.Background(), routes, root, key, request)
	if e != nil || unrecorded.Recorded || unrecorded.Evidence != nil {
		t.Fatal(unrecorded, e)
	}
	if _, e = os.Stat(root); !os.IsNotExist(e) {
		t.Fatal("inspection created evidence", e)
	}

	got, e := f.client.AutomaticStatus(context.Background(), routes, key, request)
	if e != nil || got.State != "succeeded" {
		t.Fatal(got, e)
	}
	verified, e := f.client.AutomaticOutcome(context.Background(), routes, key, request)
	if e != nil || verified.Output() != "answer" {
		t.Fatal(verified, e)
	}
	if e = verified.Record(context.Background(), root, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	if rank := remoteRank(t, root, verified); rank.AdvisorySamples != 0 || rank.ConfirmedSamples != 0 {
		t.Fatal("completion graded itself", rank)
	}
	review.ReceiptSHA256, _ = verified.Receipt().Digest()
	review.Review.ExecutionDigest, _ = verified.Receipt().Execution.Digest()
	for range 2 {
		if e = f.client.ReviewAutomaticOutcome(context.Background(), routes, root, key, request, review); e != nil {
			t.Fatal(e)
		}
	}
	if rank := remoteRank(t, root, verified); rank.AdvisorySamples != 1 || rank.ConfirmedSamples != 0 {
		t.Fatal(rank)
	}

	inspected, e := f.client.InspectAutomaticReview(context.Background(), routes, root, key, request)
	if e != nil || !inspected.Recorded || inspected.Evidence == nil || inspected.Evidence.Head.ID != review.Review.ID || inspected.Evidence.Classification != "advisory" {
		t.Fatal(inspected, e)
	}
	revised := review
	revised.Review.ID = "revision"
	revised.Review.ExpectedHead = inspected.Evidence.Head.ID
	revised.Review.Verdict = "failed"
	revised.Review.Quality = 0
	revised.Review.CreatedAt = time.Now().UTC()
	if e = f.client.ReviewAutomaticOutcome(context.Background(), routes, root, key, request, revised); e != nil {
		t.Fatal(e)
	}
	stale := revised
	stale.Review.ID = "stale"
	if e = f.client.ReviewAutomaticOutcome(context.Background(), routes, root, key, request, stale); !errors.Is(e, harness.ErrConflict) {
		t.Fatal("stale head accepted", e)
	}
	current, e := f.client.InspectAutomaticReview(context.Background(), routes, root, key, request)
	if e != nil || current.Evidence.Head.ID != "revision" || current.Evidence.Head.Verdict != "failed" {
		t.Fatal(current, e)
	}
	changed := request
	changed.Prompt = "changed"
	if _, e = f.client.AutomaticOutcome(context.Background(), routes, key, changed); !errors.Is(e, ErrConflict) {
		t.Fatal("changed intent accepted", e)
	}
	if _, e = f.client.CancelAutomatic(context.Background(), routes, key, changed); !errors.Is(e, ErrConflict) {
		t.Fatal("changed cancel intent", e)
	}
	if e = f.client.ReviewAutomaticOutcome(context.Background(), routes, root, key, changed, review); !errors.Is(e, ErrConflict) {
		t.Fatal("changed review intent", e)
	}
	// Local choice corruption must not redirect an otherwise valid bound intent.
	choice.Destination = "node-c"
	body, _ := json.Marshal(choice)
	if e = os.WriteFile(routes.choicePath(key), body, 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = routes.ResolveAutomatic(key, request); !errors.Is(e, ErrConflict) {
		t.Fatal("choice/binding disagreement", e)
	}
	if b.submits.Load() != before {
		t.Fatal("read/review submitted inference")
	}
}
func TestAutomaticControlsRejectCallerRotationAndCancelOwnedRequest(t *testing.T) {
	f, routes, request, candidates, a, b := automaticFixture(t)
	key := "automatic-control-01"
	_, _, e := f.client.DispatchAutomatic(context.Background(), routes, filepath.Join(t.TempDir(), "evidence"), key, request, harness.DefaultPolicy(), candidates[:1], 0)
	if e != nil {
		t.Fatal(e)
	}
	old := f.client.Credentials
	replacement, _ := f.ca.leaf(t, "replacement")
	f.client.Credentials = replacement
	if _, e = f.client.AutomaticStatus(context.Background(), routes, key, request); !errors.Is(e, ErrConflict) {
		t.Fatal("caller rotation accepted", e)
	}
	if _, e = f.client.CancelAutomatic(context.Background(), routes, key, request); !errors.Is(e, ErrConflict) {
		t.Fatal("rotated cancellation", e)
	}
	f.client.Credentials = old
	status, e := f.client.CancelAutomatic(context.Background(), routes, key, request)
	if e != nil || status.State != "canceled" {
		t.Fatal(status, e)
	}
	if a.creates != 1 || b.creates != 0 {
		t.Fatal("control changed destination")
	}
}
