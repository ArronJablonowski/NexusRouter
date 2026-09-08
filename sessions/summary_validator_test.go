package sessions

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSummaryValidatorRegistryAndDecisionBounds(t *testing.T) {
	valid := SummaryValidatorFunc(func(context.Context, SummaryValidationInput) (SummaryValidationDecision, error) {
		return SummaryValidationDecision{Decision: "approved", Note: "deterministic evidence passed"}, nil
	})
	input := map[string]SummaryValidator{"project-tests-v1": valid}
	registry, err := NewSummaryValidatorRegistry(input)
	if err != nil {
		t.Fatal(err)
	}
	delete(input, "project-tests-v1")
	if got, err := registry.Resolve("project-tests-v1"); err != nil || got == nil {
		t.Fatal("registry did not own mapping", err)
	}
	for _, id := range []string{"", "bad/id", strings.Repeat("a", 65)} {
		if _, err := registry.Resolve(id); err == nil {
			t.Fatal("invalid validator identity accepted", id)
		}
	}
	var nilFunc SummaryValidatorFunc
	if _, err := NewSummaryValidatorRegistry(map[string]SummaryValidator{"nil-v1": nilFunc}); err == nil {
		t.Fatal("typed nil validator accepted")
	}
	for _, decision := range []SummaryValidationDecision{
		{Decision: "approved", Note: "pass"}, {Decision: "rejected", Note: "failure"}, {Decision: "abstained", Note: "unsupported domain"},
	} {
		if err := decision.Validate(); err != nil {
			t.Fatal(decision, err)
		}
	}
	for _, decision := range []SummaryValidationDecision{{Decision: "allow", Note: "pass"}, {Decision: "approved"}, {Decision: "approved", Note: strings.Repeat("a", 4097)}} {
		if err := decision.Validate(); err == nil {
			t.Fatal("invalid decision accepted", decision.Decision)
		}
	}
}

func TestSummaryDraftDigestBindsContent(t *testing.T) {
	draft := SummaryDraft{SourceTaskID: "task", SourceSequence: 2, SourceDigest: strings.Repeat("a", 64), Model: "model", Request: CompactionRequest{Keep: 1, Summary: Summary{Requirements: []string{"retain"}}}}
	first, err := SummaryDraftDigest(draft)
	if err != nil || len(first) != 64 {
		t.Fatal(first, err)
	}
	draft.Request.Summary.Requirements[0] = "changed"
	second, err := SummaryDraftDigest(draft)
	if err != nil || first == second {
		t.Fatal("draft mutation did not change digest", err)
	}
}

func TestValidatedReviewRequiresEvidenceAndManualReviewCannotAbstain(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	manual := SummaryReview{Version: 1, ID: "manual", AttemptID: "attempt", Decision: "abstained", Note: "unsupported", Time: now}
	if manual.Validate() == nil {
		t.Fatal("manual abstention accepted")
	}
	validated := SummaryReview{Version: 2, ID: "validation", AttemptID: "attempt", Decision: "abstained", Note: "unsupported", ValidatorID: "project-tests-v1", SourceSequence: 1, SourceDigest: strings.Repeat("a", 64), DraftDigest: strings.Repeat("b", 64), Time: now}
	if err := validated.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SummaryReview){
		func(r *SummaryReview) { r.ValidatorID = "" },
		func(r *SummaryReview) { r.SourceSequence = 0 },
		func(r *SummaryReview) { r.SourceDigest = strings.Repeat("A", 64) },
		func(r *SummaryReview) { r.DraftDigest = strings.Repeat("z", 64) },
	} {
		r := validated
		mutate(&r)
		if r.Validate() == nil {
			t.Fatal("invalid validation evidence accepted", r)
		}
	}
}
