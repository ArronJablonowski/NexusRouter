package evaluation

import (
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func digestAuditFixture() AuditRecord {
	return AuditRecord{Version: 1, ID: "audit", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "reviewer-model",
		EvaluatorProvider: "provider", Audit: Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: "rubric",
			Domain: "code", Verdict: "reject", Confidence: .75,
			Findings: []AuditFinding{{Summary: "missing result", EvidenceRefs: []string{"candidate"}}}},
		EvidenceRefs: []string{"requirements", "candidate"}, Usage: &providers.Usage{InputTokens: 10, OutputTokens: 2},
		Elapsed: time.Second, Time: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)}
}

func TestAuditRecordDigestCanonicalAndComplete(t *testing.T) {
	record := digestAuditFixture()
	want, err := AuditRecordDigest(record)
	if err != nil || len(want) != 64 || want != strings.ToLower(want) {
		t.Fatalf("invalid digest: %q %v", want, err)
	}
	offset := record
	offset.Time = record.Time.In(time.FixedZone("offset", -6*60*60))
	if got, err := AuditRecordDigest(offset); err != nil || got != want {
		t.Fatalf("equal instant changed digest: %q %v", got, err)
	}

	mutations := map[string]func(*AuditRecord){
		"id":            func(r *AuditRecord) { r.ID = "other-audit" },
		"task":          func(r *AuditRecord) { r.TaskID = "other-task" },
		"attempt":       func(r *AuditRecord) { r.AttemptID = "other-attempt" },
		"model":         func(r *AuditRecord) { r.EvaluatorModel = "other-model" },
		"provider":      func(r *AuditRecord) { r.EvaluatorProvider = "other-provider" },
		"verdict":       func(r *AuditRecord) { r.Audit.Verdict = "accept" },
		"confidence":    func(r *AuditRecord) { r.Audit.Confidence = .5 },
		"finding":       func(r *AuditRecord) { r.Audit.Findings[0].Summary = "different finding" },
		"finding refs":  func(r *AuditRecord) { r.Audit.Findings[0].EvidenceRefs = []string{"requirements"} },
		"evidence refs": func(r *AuditRecord) { r.EvidenceRefs = append(r.EvidenceRefs, "execution") },
		"usage":         func(r *AuditRecord) { r.Usage.OutputTokens++ },
		"elapsed":       func(r *AuditRecord) { r.Elapsed += time.Millisecond },
		"time":          func(r *AuditRecord) { r.Time = r.Time.Add(time.Second) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := digestAuditFixture()
			mutate(&changed)
			got, err := AuditRecordDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("mutation did not alter digest")
			}
		})
	}
}

func TestAuditRecordDigestRejectsInvalidRecord(t *testing.T) {
	record := digestAuditFixture()
	record.ID = ""
	if digest, err := AuditRecordDigest(record); err == nil || digest != "" {
		t.Fatalf("invalid record digested: %q %v", digest, err)
	}
}
