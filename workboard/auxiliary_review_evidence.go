package workboard

import (
	"strconv"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
)

// ValidateAuxiliaryReviewEvidence proves that advisory evidence is an exact
// host projection of the structured audit. Model output cannot choose evidence
// source, actor, criterion identity, or a result inconsistent with its verdict.
func ValidateAuxiliaryReviewEvidence(frozen CandidateEvaluationRequest, audit evaluation.AuditRecord,
	evidence []EvidenceInput,
) error {
	if frozen.Validate() != nil || frozen.BindingKind != "runtime_budgeted" || audit.Validate() != nil ||
		audit.TaskID != frozen.SourceTaskID || audit.AttemptID != frozen.SourceAttemptID || audit.Audit.Domain != frozen.SourceDomain {
		return fail(CodeInvalid, "auxiliary_review_evidence")
	}
	refs := []string{"requirements", "candidate", "candidate_claim", "source_binding"}
	criteriaByRef := make(map[string]string, len(frozen.Criteria))
	for index, criterion := range frozen.Criteria {
		ref := "criterion_" + twoDigitCriterionIndex(index)
		refs = append(refs, ref)
		criteriaByRef[ref] = criterion.ID
	}
	if !sameStrings(audit.EvidenceRefs, refs) {
		return fail(CodeInvalid, "auxiliary_review_evidence")
	}
	cited := make(map[string]bool, len(frozen.Criteria))
	for _, finding := range audit.Audit.Findings {
		for _, ref := range finding.EvidenceRefs {
			if criterion := criteriaByRef[ref]; criterion != "" {
				cited[criterion] = true
			}
		}
	}
	if audit.Audit.Verdict == "abstain" {
		if len(evidence) != 0 {
			return fail(CodeInvalid, "auxiliary_review_evidence")
		}
		return nil
	}
	wantOutcome := "passed"
	if audit.Audit.Verdict == "reject" {
		wantOutcome = "failed"
	}
	if len(evidence) != len(cited) {
		return fail(CodeInvalid, "auxiliary_review_evidence")
	}
	seen := make(map[string]bool, len(evidence))
	for _, item := range evidence {
		if item.Validate() != nil || !cited[item.CriterionID] || seen[item.CriterionID] || item.Source != "model_audit" ||
			item.Outcome != wantOutcome || item.ActorID != audit.Audit.EvaluatorID || item.ActorType != "model" ||
			item.Reference != audit.ID {
			return fail(CodeInvalid, "auxiliary_review_evidence")
		}
		seen[item.CriterionID] = true
	}
	return nil
}

func twoDigitCriterionIndex(index int) string {
	if index < 10 {
		return "0" + strconv.Itoa(index)
	}
	return strconv.Itoa(index)
}
