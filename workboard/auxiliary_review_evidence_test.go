package workboard

import (
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
)

func auxiliaryEvidenceAudit(frozen CandidateEvaluationRequest, verdict string, findings []evaluation.AuditFinding) evaluation.AuditRecord {
	refs := []string{"requirements", "candidate", "candidate_claim", "source_binding", "criterion_00"}
	return evaluation.AuditRecord{Version: 1, ID: "audit", TaskID: frozen.SourceTaskID, AttemptID: frozen.SourceAttemptID,
		EvaluatorModel: "review-model", EvaluatorProvider: "review-provider",
		Audit: evaluation.Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: "rubric", Domain: frozen.SourceDomain,
			Verdict: verdict, Confidence: .5, Findings: findings}, EvidenceRefs: refs, Time: time.Unix(200, 0).UTC()}
}

func auxiliaryEvidenceFindings() []evaluation.AuditFinding {
	return []evaluation.AuditFinding{{Summary: "criterion result", EvidenceRefs: []string{"criterion_00"}}}
}

func TestAuxiliaryReviewEvidenceMustBeExactAuditProjection(t *testing.T) {
	frozen, _, _, _ := auxiliaryReviewOutcomeFixtures(t)
	findings := auxiliaryEvidenceFindings()
	audit := auxiliaryEvidenceAudit(frozen, "reject", findings)
	valid := []EvidenceInput{{CriterionID: "tests", Source: "model_audit", Outcome: "failed", ActorID: "reviewer", ActorType: "model", Reference: "audit"}}
	if err := ValidateAuxiliaryReviewEvidence(frozen, audit, valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*evaluation.AuditRecord, *[]EvidenceInput){
		"abstain with evidence": func(a *evaluation.AuditRecord, _ *[]EvidenceInput) {
			a.Audit.Verdict = "abstain"
			a.Audit.Findings = nil
		},
		"uncited criterion": func(a *evaluation.AuditRecord, _ *[]EvidenceInput) {
			a.Audit.Findings[0].EvidenceRefs = []string{"candidate"}
		},
		"reject passed":          func(_ *evaluation.AuditRecord, e *[]EvidenceInput) { (*e)[0].Outcome = "passed" },
		"accept failed":          func(a *evaluation.AuditRecord, _ *[]EvidenceInput) { a.Audit.Verdict = "accept" },
		"duplicate":              func(_ *evaluation.AuditRecord, e *[]EvidenceInput) { *e = append(*e, (*e)[0]) },
		"transplanted criterion": func(_ *evaluation.AuditRecord, e *[]EvidenceInput) { (*e)[0].CriterionID = "other" },
		"allowed refs drift": func(a *evaluation.AuditRecord, _ *[]EvidenceInput) {
			a.EvidenceRefs = []string{"requirements", "candidate"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changedAudit := auxiliaryEvidenceAudit(frozen, "reject", auxiliaryEvidenceFindings())
			changedEvidence := append([]EvidenceInput(nil), valid...)
			mutate(&changedAudit, &changedEvidence)
			if err := ValidateAuxiliaryReviewEvidence(frozen, changedAudit, changedEvidence); err == nil {
				t.Fatal("hostile evidence projection accepted")
			}
		})
	}
}
