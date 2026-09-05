package evaluation

import (
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// AuditRecord stores an advisory review independently of candidate fitness.
// The host supplies attribution, timing, evidence references and measured usage;
// model output supplies only Audit. Sensitive findings require host redaction.
type AuditRecord struct {
	Version                           int
	ID, TaskID, AttemptID             string
	EvaluatorModel, EvaluatorProvider string
	Audit                             Audit
	EvidenceRefs                      []string
	Usage                             *providers.Usage
	Elapsed                           time.Duration
	Time                              time.Time
}

func (r AuditRecord) Validate() error {
	if r.Version != 1 || !auditLabel(r.ID) || !auditLabel(r.TaskID) || !auditLabel(r.AttemptID) || !auditLabel(r.EvaluatorModel) || !auditLabel(r.EvaluatorProvider) || r.Time.IsZero() || r.Elapsed < 0 || r.Elapsed > time.Minute {
		return ErrAudit
	}
	if r.Usage != nil && (r.Usage.InputTokens < 0 || r.Usage.OutputTokens < 0) {
		return ErrAudit
	}
	body, err := json.Marshal(r.Audit)
	if err != nil {
		return ErrAudit
	}
	_, err = ParseAudit(body, AuditContext{EvaluatorID: r.Audit.EvaluatorID, RubricVersion: r.Audit.RubricVersion, Domain: r.Audit.Domain, AllowedEvidenceRefs: r.EvidenceRefs})
	return err
}
