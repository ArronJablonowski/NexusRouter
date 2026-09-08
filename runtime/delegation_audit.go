package runtime

import (
	"errors"
	"math"
)

// DelegationAuditIntent records that a worker output must pass through one
// bounded advisory audit before it can be published. It contains only stable
// configured identities; prompts and candidate content belong to the child
// task and are never copied into the worker journal.
type DelegationAuditIntent struct {
	Version     int    `json:"version"`
	OperationID string `json:"operation_id"`
	ReviewerID  string `json:"reviewer_id"`
}

func (i DelegationAuditIntent) Validate() error {
	if i.Version != 1 || !validDelegationAuditID(i.OperationID, 128) || !validDelegationAuditID(i.ReviewerID, 128) {
		return errors.New("invalid delegation audit intent")
	}
	return nil
}

// DelegationAudit is the sanitized, durable worker-side projection of an
// advisory review. Citations are opaque evidence identifiers, not evidence
// content. Confidence is a model self-report and is never acceptance evidence.
type DelegationAudit struct {
	Version     int      `json:"version"`
	OperationID string   `json:"operation_id"`
	ReviewerID  string   `json:"reviewer_id"`
	AuditID     string   `json:"audit_id,omitempty"`
	Status      string   `json:"status"`
	Verdict     string   `json:"verdict,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
	Citations   []string `json:"cited_evidence"`
}

func (a DelegationAudit) Validate(intent *DelegationAuditIntent) error {
	if a.Version != 1 || !validDelegationAuditID(a.OperationID, 128) || !validDelegationAuditID(a.ReviewerID, 128) || a.Citations == nil || len(a.Citations) > 256 || intent == nil || intent.Validate() != nil || a.OperationID != intent.OperationID || a.ReviewerID != intent.ReviewerID {
		return errors.New("invalid delegation audit")
	}
	seen := make(map[string]bool, len(a.Citations))
	for _, citation := range a.Citations {
		if !validDelegationAuditID(citation, 128) || seen[citation] {
			return errors.New("invalid delegation audit citation")
		}
		seen[citation] = true
	}
	switch a.Status {
	case "completed", "rejected", "abstained":
		want := map[string]string{"completed": "accept", "rejected": "reject", "abstained": "abstain"}[a.Status]
		if a.Verdict != want || !validDelegationAuditID(a.AuditID, 128) || a.Confidence == nil || math.IsNaN(*a.Confidence) || math.IsInf(*a.Confidence, 0) || *a.Confidence < 0 || *a.Confidence > 1 || (a.Status != "abstained" && len(a.Citations) == 0) {
			return errors.New("invalid completed delegation audit")
		}
	case "failed", "not_run":
		if a.AuditID != "" || a.Verdict != "" || a.Confidence != nil || len(a.Citations) != 0 {
			return errors.New("invalid unavailable delegation audit")
		}
	default:
		return errors.New("invalid delegation audit status")
	}
	return nil
}

func (a DelegationAudit) Clone() *DelegationAudit {
	copy := a
	copy.Citations = append([]string(nil), a.Citations...)
	if a.Confidence != nil {
		confidence := *a.Confidence
		copy.Confidence = &confidence
	}
	return &copy
}

func validDelegationAuditID(id string, limit int) bool {
	if len(id) == 0 || len(id) > limit {
		return false
	}
	for i, b := range []byte(id) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || (i > 0 && (b == '_' || b == '.' || b == '-')) {
			continue
		}
		return false
	}
	return true
}
