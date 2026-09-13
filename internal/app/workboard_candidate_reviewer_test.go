package app

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type workboardReviewProvider struct {
	calls     atomic.Int32
	response  string
	request   providers.Request
	usage     *providers.Usage
	omitUsage bool
}

func (p *workboardReviewProvider) Models(context.Context) ([]string, error) {
	return []string{"review-native"}, nil
}

func (p *workboardReviewProvider) Stream(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	p.calls.Add(1)
	p.request = request
	usage := p.usage
	if usage == nil && !p.omitUsage {
		usage = &providers.Usage{InputTokens: 40, OutputTokens: 10}
	}
	if err := emit(providers.Chunk{Text: p.response, Usage: usage}); err != nil {
		return err
	}
	return emit(providers.Chunk{Done: true, FinishReason: "stop"})
}

func workboardReviewFrozen(t *testing.T) workboard.CandidateEvaluationRequest {
	t.Helper()
	criteria := []workboard.AcceptanceCriterion{
		{Version: workboard.SchemaVersion, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "All tests pass.", Required: true},
		{Version: workboard.SchemaVersion, ID: "tone", Kind: "subjective", RequiredSource: "user_feedback", ValidatorID: "operator", Description: "The tone is helpful.", Required: true},
	}
	summary := "Implemented the requested change."
	sourceOutput := "The runtime model returned this exact result."
	artifacts := []string{}
	return workboard.CandidateEvaluationRequest{
		Version: workboard.SchemaVersion, BoardID: "board", CardID: "card", AttemptID: "work-attempt", ClaimID: "claim", CandidateID: "candidate",
		BindingKind: "runtime_budgeted", SourceTaskID: "task", SourceSessionID: "session", SourceTurnID: "turn", SourceAttemptID: "runtime-attempt",
		SourceCompletionEventID: "turn-complete", SourceCompletionSequence: 3, SourceCompletionDigest: strings.Repeat("1", 64), SourceOutput: sourceOutput, SourceOutputDigest: workboard.SourceOutputDigest(sourceOutput),
		SourceTerminalEventID: "task-complete", SourceTerminalSequence: 4, SourceTerminalDigest: strings.Repeat("3", 64),
		SourceDomain: "code", SourceProfile: "default", SourcePrivacy: "local_only", AdmissionID: "execution-admission", AdmissionDigest: strings.Repeat("4", 64),
		SourceModelID: "worker-native", SourceProviderID: "local", ConfigID: strings.Repeat("5", 64), SourceTimeLimitMS: 5000, SourceTokenLimit: 1000,
		SourceCostMicros: 100, WorkerID: "worker", ExpectedCardRevision: 2, ExpectedClaimRevision: 1, CriteriaRevision: 1,
		CandidateDigest: workboard.CandidateContentDigest(summary, artifacts), CriteriaDigest: workboard.AcceptanceCriteriaDigest(criteria), PolicyDigest: strings.Repeat("6", 64),
		Summary: summary, ArtifactRefs: artifacts, Criteria: criteria,
	}
}

func workboardReviewerConfig() workboardCandidateReviewerConfig {
	return workboardCandidateReviewerConfig{
		ReviewerID: "reviewer", ModelID: "review-native", ProviderID: "local", ConfigID: strings.Repeat("7", 64),
		Local:         true,
		ContextTokens: 100_000, MaxInputTokens: 90_000, MaxOutputTokens: 1_000, Timeout: time.Second,
		EstimatedCost: 0.1, MaxCost: 0.25, StructuredOutput: true,
	}
}

func workboardAuditResponse(verdict string, findings any) string {
	body, _ := json.Marshal(map[string]any{"version": 1, "evaluator_id": "reviewer", "rubric_version": "darwin-review-v2", "domain": "code", "verdict": verdict, "confidence": .8, "findings": findings})
	return string(body)
}

func TestWorkboardCandidateReviewerProducesBoundAdvisoryAuditAndEvidence(t *testing.T) {
	provider := &workboardReviewProvider{response: workboardAuditResponse("reject", []any{
		map[string]any{"summary": "No test result supports this claim.", "evidence_refs": []string{"criterion_00", "source_binding"}},
	})}
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
	reviewer.now = func() time.Time { return time.Unix(100, 0).UTC() }
	reviewer.newID = func() string { return "audit" }
	frozen := workboardReviewFrozen(t)

	reservation, err := reviewer.AuxiliaryReviewReservation(frozen)
	if err != nil || reservation.TokenLimit != 91_000 || reservation.CostMicros != 250_000 || reservation.ReviewerID != "reviewer" {
		t.Fatalf("invalid reservation: %+v %v frozen=%v config=%v", reservation, err, frozen.Validate(), reviewer.invalidConfiguration())
	}
	result, err := reviewer.review(context.Background(), frozen)
	if err != nil || result.Evaluation.Audit.Validate() != nil || result.Evaluation.Audit.TaskID != frozen.SourceTaskID || result.Evaluation.Audit.AttemptID != frozen.SourceAttemptID ||
		result.Evaluation.Audit.ID != "audit" || result.Evaluation.Audit.Audit.Verdict != "reject" || len(result.Evaluation.Evidence) != 1 {
		t.Fatalf("invalid review result: %+v %v", result, err)
	}
	evidence := result.Evaluation.Evidence[0]
	if evidence.CriterionID != "tests" || evidence.Source != "model_audit" || evidence.Outcome != "failed" || evidence.ActorID != "reviewer" || evidence.ActorType != "model" || evidence.Reference != "audit" {
		t.Fatalf("untrusted evidence projection: %+v", evidence)
	}
	if result.Evaluation.Measurements.Tokens == nil || *result.Evaluation.Measurements.Tokens != 50 || result.Evaluation.Measurements.TimeMS == nil || result.Evaluation.Measurements.CostMicros != nil {
		t.Fatalf("measurements not provider-derived/conservative: %+v", result.Evaluation.Measurements)
	}
	if provider.calls.Load() != 1 || len(provider.request.Tools) != 0 || provider.request.MaxOutputTokens != 1_000 || len(provider.request.JSONSchema) == 0 {
		t.Fatalf("review was not one bounded tool-free call: calls=%d request=%+v", provider.calls.Load(), provider.request)
	}
	if len(provider.request.Messages) != 2 || !strings.Contains(provider.request.Messages[1].Content, frozen.SourceOutputDigest) || !strings.Contains(provider.request.Messages[1].Content, "user_feedback") {
		t.Fatal("exact output binding or subjective evidence boundary missing from review input")
	}
	if !strings.Contains(provider.request.Messages[1].Content, frozen.SourceOutput) || !strings.Contains(provider.request.Messages[1].Content, frozen.Summary) ||
		!strings.Contains(provider.request.Messages[1].Content, `"id":"candidate_claim"`) {
		t.Fatal("reviewer did not receive the exact runtime output and bound candidate claim")
	}
}

func TestWorkboardCandidateReviewerKeepsSubjectiveModelOpinionAdvisory(t *testing.T) {
	provider := &workboardReviewProvider{response: workboardAuditResponse("accept", []any{
		map[string]any{"summary": "Tone seems helpful.", "evidence_refs": []string{"criterion_01"}},
	})}
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
	reviewer.newID = func() string { return "subjective-audit" }
	result, err := reviewer.review(context.Background(), workboardReviewFrozen(t))
	if err != nil || len(result.Evaluation.Evidence) != 1 {
		t.Fatalf("subjective advisory review failed: %+v %v", result, err)
	}
	evidence := result.Evaluation.Evidence[0]
	if evidence.CriterionID != "tone" || evidence.Source != "model_audit" || evidence.Outcome != "passed" {
		t.Fatalf("subjective opinion gained user authority: %+v", evidence)
	}
}

func TestWorkboardCandidateReviewerCannotMaskEmptyRuntimeOutputWithSummary(t *testing.T) {
	provider := &workboardReviewProvider{response: workboardAuditResponse("reject", []any{
		map[string]any{"summary": "The runtime returned no meaningful output.", "evidence_refs": []string{"candidate", "source_binding"}},
	})}
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
	reviewer.newID = func() string { return "empty-output-audit" }
	frozen := workboardReviewFrozen(t)
	frozen.Summary = "A plausible but independently supplied completion summary."
	frozen.CandidateDigest = workboard.CandidateContentDigest(frozen.Summary, frozen.ArtifactRefs)
	frozen.SourceOutput = ""
	frozen.SourceOutputDigest = workboard.SourceOutputDigest("")
	result, err := reviewer.review(context.Background(), frozen)
	if err != nil || result.Evaluation.Audit.Audit.Verdict != "reject" || len(result.Evaluation.Evidence) != 0 {
		t.Fatalf("empty runtime output was not audited exactly: %+v %v", result, err)
	}
	if !strings.Contains(provider.request.Messages[1].Content, frozen.Summary) ||
		!strings.Contains(provider.request.Messages[1].Content, `"id":"candidate","content":""`) ||
		!strings.Contains(provider.request.Messages[1].Content, `"id":"candidate_claim"`) {
		t.Fatal("independent candidate summary masked empty runtime output")
	}
}

func TestWorkboardCandidateReviewerAbstainAndUncitedCriteriaCreateNoEvidence(t *testing.T) {
	for name, response := range map[string]string{
		"abstain": workboardAuditResponse("abstain", []any{}),
		"uncited": workboardAuditResponse("reject", []any{map[string]any{"summary": "Binding is suspicious.", "evidence_refs": []string{"source_binding"}}}),
	} {
		t.Run(name, func(t *testing.T) {
			provider := &workboardReviewProvider{response: response}
			reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
			reviewer.newID = func() string { return "audit" }
			result, err := reviewer.review(context.Background(), workboardReviewFrozen(t))
			if err != nil || len(result.Evaluation.Evidence) != 0 || result.Evaluation.Audit.Validate() != nil {
				t.Fatalf("review invented criterion evidence: %+v %v", result, err)
			}
		})
	}
}

func TestWorkboardCandidateReviewerFailsClosedBeforeOrAfterProvider(t *testing.T) {
	t.Run("non-finite cost", func(t *testing.T) {
		provider := &workboardReviewProvider{}
		config := workboardReviewerConfig()
		config.EstimatedCost = math.NaN()
		reviewer := newWorkboardCandidateReviewer(config, provider, nil, nil)
		if _, err := reviewer.AuxiliaryReviewReservation(workboardReviewFrozen(t)); !errors.Is(err, ErrAdmission) || provider.calls.Load() != 0 {
			t.Fatalf("non-finite cost admitted: calls=%d err=%v", provider.calls.Load(), err)
		}
	})

	t.Run("non-runtime candidate", func(t *testing.T) {
		provider := &workboardReviewProvider{}
		reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
		frozen := workboardReviewFrozen(t)
		frozen.BindingKind = "runtime_unbudgeted"
		frozen.SourceTurnID, frozen.SourceAttemptID, frozen.SourceCompletionEventID, frozen.SourceCompletionDigest, frozen.SourceOutput, frozen.SourceOutputDigest = "", "", "", "", "", ""
		frozen.SourceCompletionSequence, frozen.SourceTerminalSequence = 0, 0
		frozen.SourceTerminalEventID, frozen.SourceTerminalDigest, frozen.SourceDomain, frozen.SourceProfile, frozen.SourcePrivacy = "", "", "", "", ""
		frozen.AdmissionID, frozen.AdmissionDigest, frozen.SourceModelID, frozen.SourceProviderID, frozen.ConfigID = "", "", "", "", ""
		frozen.SourceTimeLimitMS, frozen.SourceTokenLimit, frozen.SourceCostMicros = 0, 0, 0
		if _, err := reviewer.EvaluateBudgetedCandidate(context.Background(), frozen); !errors.Is(err, ErrAdmission) || provider.calls.Load() != 0 {
			t.Fatalf("unbudgeted review reached provider: calls=%d err=%v", provider.calls.Load(), err)
		}
	})

	t.Run("same model", func(t *testing.T) {
		provider := &workboardReviewProvider{}
		config := workboardReviewerConfig()
		config.ModelID = "worker-native"
		reviewer := newWorkboardCandidateReviewer(config, provider, nil, nil)
		if _, err := reviewer.AuxiliaryReviewReservation(workboardReviewFrozen(t)); !errors.Is(err, ErrAdmission) || provider.calls.Load() != 0 {
			t.Fatalf("non-independent review admitted: calls=%d err=%v", provider.calls.Load(), err)
		}
	})

	t.Run("input ceiling", func(t *testing.T) {
		provider := &workboardReviewProvider{}
		config := workboardReviewerConfig()
		config.MaxInputTokens, config.MaxOutputTokens, config.ContextTokens = 100, 1, 101
		reviewer := newWorkboardCandidateReviewer(config, provider, nil, nil)
		if _, err := reviewer.EvaluateBudgetedCandidate(context.Background(), workboardReviewFrozen(t)); !errors.Is(err, ErrAdmission) || provider.calls.Load() != 0 {
			t.Fatalf("oversize review reached provider: calls=%d err=%v", provider.calls.Load(), err)
		}
	})

	t.Run("malformed output", func(t *testing.T) {
		provider := &workboardReviewProvider{response: "not-json"}
		reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
		result, err := reviewer.EvaluateBudgetedCandidate(context.Background(), workboardReviewFrozen(t))
		if !errors.Is(err, ErrAdmission) || len(result.Evidence) != 0 || result.Measurements.Tokens == nil || *result.Measurements.Tokens != 50 || result.Measurements.CostMicros != nil {
			t.Fatalf("malformed output gained evidence or lost accounting: %+v %v", result, err)
		}
	})

	t.Run("reported input ceiling", func(t *testing.T) {
		provider := &workboardReviewProvider{response: workboardAuditResponse("abstain", []any{}),
			usage: &providers.Usage{InputTokens: 90_001, OutputTokens: 10}}
		reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
		result, err := reviewer.EvaluateBudgetedCandidate(context.Background(), workboardReviewFrozen(t))
		if !errors.Is(err, ErrAdmission) || provider.calls.Load() != 1 || result.Measurements.Tokens == nil || *result.Measurements.Tokens != 90_011 {
			t.Fatalf("reported input overrun accepted or accounting lost: calls=%d result=%+v err=%v", provider.calls.Load(), result, err)
		}
	})

	t.Run("missing usage", func(t *testing.T) {
		provider := &workboardReviewProvider{response: workboardAuditResponse("abstain", []any{}), omitUsage: true}
		reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
		result, err := reviewer.EvaluateBudgetedCandidate(context.Background(), workboardReviewFrozen(t))
		if !errors.Is(err, ErrAdmission) || provider.calls.Load() != 1 || result.Measurements.Tokens != nil || len(result.Evidence) != 0 {
			t.Fatalf("review without trusted usage accepted: calls=%d result=%+v err=%v", provider.calls.Load(), result, err)
		}
	})
}

func TestWorkboardCandidateReviewerRefreshesSecretRedaction(t *testing.T) {
	const secret = "rotated-review-secret"
	provider := &workboardReviewProvider{response: workboardAuditResponse("reject", []any{
		map[string]any{"summary": "Finding contains " + secret, "evidence_refs": []string{"criterion_00"}},
	})}
	var resolutions atomic.Int32
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, func() []string {
		if resolutions.Add(1) >= 3 {
			return []string{secret}
		}
		return nil
	})
	reviewer.newID = func() string { return "audit" }
	result, err := reviewer.review(context.Background(), workboardReviewFrozen(t))
	if err != nil || strings.Contains(result.Evaluation.Audit.Audit.Findings[0].Summary, secret) || !strings.Contains(result.Evaluation.Audit.Audit.Findings[0].Summary, "[REDACTED]") {
		t.Fatalf("rotated secret was not removed: %+v %v", result, err)
	}
}

func TestWorkboardCandidateReviewerContainsSecretResolverPanic(t *testing.T) {
	provider := &workboardReviewProvider{}
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, func() []string { panic("private") })
	if _, err := reviewer.EvaluateBudgetedCandidate(context.Background(), workboardReviewFrozen(t)); !errors.Is(err, ErrAdmission) || provider.calls.Load() != 0 {
		t.Fatalf("secret resolver panic escaped or reached provider: calls=%d err=%v", provider.calls.Load(), err)
	}
}
