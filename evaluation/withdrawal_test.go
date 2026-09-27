package evaluation

import "testing"

func TestWithdrawalIsNotQualityAndCannotHideObjectiveEvidence(t *testing.T) {
	prior, next := revisionFixture()
	next.Checks = []Check{{Source: Withdrawn, Reference: "invalid-fixture"}}
	if err := ValidateRevision(prior, next); err != nil {
		t.Fatal(err)
	}
	out, err := Resolve(next.Checks, false)
	if err != nil || out.Source != Withdrawn || out.Accepted {
		t.Fatal(out, err)
	}
	for _, checks := range [][]Check{
		{{Source: Withdrawn, Reference: "invalid-fixture", Passed: true}},
		{{Source: Withdrawn, Reference: "invalid-fixture"}, {Source: UserFeedback, Reference: "other"}},
	} {
		if _, err := Resolve(checks, false); err == nil {
			t.Fatal("invalid withdrawal accepted")
		}
	}
	if _, err := Resolve(next.Checks, true); err == nil {
		t.Fatal("judge can withdraw")
	}
	prior.AllowJudge = false
	prior.Checks = []Check{{Source: Deterministic, Reference: "test", Passed: false}}
	if ValidateRevision(prior, next) == nil {
		t.Fatal("objective evidence withdrawn")
	}
}

func TestWithdrawnEvidenceIsExcludedFromDeprecation(t *testing.T) {
	_, record := revisionFixture()
	record.Checks = []Check{{Source: Withdrawn, Reference: "fixture-withdrawal"}}
	report, err := SummarizeDeprecation([]Record{record}, DeprecationPolicy{Window: 10, MinSamples: 1, FailureThreshold: .1})
	if err != nil || report.Validate() != nil || report.EligibleSamples != 0 || report.Failures != 0 || report.ExcludedWithdrawn != 1 || report.Candidate {
		t.Fatal(report, err)
	}
}
