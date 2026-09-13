package workboard

import "testing"

func TestBudgetedCandidateEvidenceRequiresConfiguredValidatorIdentity(t *testing.T) {
	frozen, _, _, _ := auxiliaryReviewOutcomeFixtures(t)
	audit := auxiliaryEvidenceAudit(frozen, "reject", auxiliaryEvidenceFindings())
	valid := []EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "passed",
		ActorID: "go-test", ActorType: "validator", Reference: "trusted-event"},
		{CriterionID: "tests", Source: "model_audit", Outcome: "failed", ActorID: "reviewer", ActorType: "model", Reference: "audit"}}
	if err := ValidateBudgetedCandidateEvidence(frozen, audit, valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*EvidenceInput){
		"wrong validator": func(e *EvidenceInput) { e.ActorID = "other-validator" },
		"user feedback":   func(e *EvidenceInput) { e.Source, e.ActorID, e.ActorType = "user_feedback", "operator", "operator" },
		"duplicate":       func(e *EvidenceInput) {},
	} {
		t.Run(name, func(t *testing.T) {
			changed := append([]EvidenceInput(nil), valid...)
			mutate(&changed[0])
			if name == "duplicate" {
				changed = append(changed, changed[0])
			}
			if ValidateBudgetedCandidateEvidence(frozen, audit, changed) == nil {
				t.Fatal("untrusted deterministic evidence accepted")
			}
		})
	}
}
