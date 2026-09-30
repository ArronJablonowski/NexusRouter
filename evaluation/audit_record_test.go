package evaluation

import (
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestAuditRecordValidation(t *testing.T) {
	r := AuditRecord{Version: 1, ID: "audit", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "reviewer", EvaluatorProvider: "local", EvidenceRefs: []string{"candidate"}, Time: time.Now(), Audit: Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: "v1", Domain: "creative", Verdict: "abstain", Findings: []AuditFinding{}}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Usage = &providers.Usage{InputTokens: -1}
	if r.Validate() == nil {
		t.Fatal("negative usage accepted")
	}
	r.Usage = nil
	r.Audit.Verdict = "accept"
	r.Audit.Findings = []AuditFinding{{Summary: "invented", EvidenceRefs: []string{"missing"}}}
	if r.Validate() == nil {
		t.Fatal("invented reference accepted")
	}
}
