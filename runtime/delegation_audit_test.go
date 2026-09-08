package runtime

import "testing"

func TestDelegationAuditContract(t *testing.T) {
	intent := &DelegationAuditIntent{Version: 1, OperationID: "operation", ReviewerID: "reviewer"}
	confidence := .75
	valid := DelegationAudit{Version: 1, OperationID: "operation", ReviewerID: "reviewer", AuditID: "audit", Status: "rejected", Verdict: "reject", Confidence: &confidence, Citations: []string{"candidate"}}
	if intent.Validate() != nil || valid.Validate(intent) != nil || valid.Clone().Validate(intent) != nil {
		t.Fatal("valid audit contract rejected")
	}
	changedIntent := *intent
	changedIntent.ReviewerID = "other"
	if valid.Validate(&changedIntent) == nil {
		t.Fatal("audit accepted under a different configured reviewer")
	}
	for name, mutate := range map[string]func(*DelegationAudit){
		"operation":  func(a *DelegationAudit) { a.OperationID = "other" },
		"reviewer":   func(a *DelegationAudit) { a.ReviewerID = "other" },
		"status":     func(a *DelegationAudit) { a.Status = "pending" },
		"verdict":    func(a *DelegationAudit) { a.Verdict = "accept" },
		"confidence": func(a *DelegationAudit) { n := 2.0; a.Confidence = &n },
		"citation":   func(a *DelegationAudit) { a.Citations = []string{"raw secret\n"} },
	} {
		t.Run(name, func(t *testing.T) {
			copy := valid
			mutate(&copy)
			if copy.Validate(intent) == nil {
				t.Fatal("invalid audit accepted")
			}
		})
	}
	for _, status := range []string{"failed", "not_run"} {
		if a := (DelegationAudit{Version: 1, OperationID: "operation", ReviewerID: "reviewer", Status: status, Citations: []string{}}); a.Validate(intent) != nil {
			t.Fatal(status, a.Validate(intent))
		}
	}
}
