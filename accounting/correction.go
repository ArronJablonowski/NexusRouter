package accounting

import "time"

type CorrectionReason string

const (
	ProviderReconciliation CorrectionReason = "provider_reconciliation"
	UsageReconciliation    CorrectionReason = "usage_reconciliation"
	PricingReconciliation  CorrectionReason = "pricing_reconciliation"
	OperatorCorrection     CorrectionReason = "operator_correction"
)

func (r CorrectionReason) Valid() bool {
	return r == ProviderReconciliation || r == UsageReconciliation || r == PricingReconciliation || r == OperatorCorrection
}

// Correction preserves the original execution identity while revising only
// measured usage/pricing. Its evidence is an opaque billing/operator receipt.
type Correction struct {
	Version    int              `json:"version"`
	ID         string           `json:"id"`
	BaseID     string           `json:"base_id"`
	Supersedes string           `json:"supersedes"`
	Evidence   string           `json:"evidence"`
	Reason     CorrectionReason `json:"reason"`
	Record     Record           `json:"record"`
	RecordedAt time.Time        `json:"recorded_at"`
}

func (c Correction) Validate(prior Record) error {
	if c.Version != 1 || !label(c.ID, 128) || !label(c.BaseID, 128) || !label(c.Supersedes, 128) || !label(c.Evidence, 128) || !c.Reason.Valid() || c.RecordedAt.IsZero() || c.RecordedAt.Location() != time.UTC || prior.Validate() != nil || c.Record.Validate() != nil || c.ID != c.Record.ID || c.ID == c.Supersedes || c.BaseID == c.ID {
		return ErrUsage
	}
	if prior.ID != c.Supersedes || prior.TaskID != c.Record.TaskID || prior.SessionID != c.Record.SessionID || prior.OperationID != c.Record.OperationID || prior.CandidateAttemptID != c.Record.CandidateAttemptID || prior.RouteID != c.Record.RouteID || prior.EvidenceID != c.Record.EvidenceID || prior.AuditID != c.Record.AuditID || prior.Provider != c.Record.Provider || prior.Model != c.Record.Model || prior.Role != c.Record.Role || prior.EvidenceKind != c.Record.EvidenceKind || prior.Disposition != c.Record.Disposition || prior.RetryClass != c.Record.RetryClass || !prior.OccurredAt.Equal(c.Record.OccurredAt) {
		return ErrUsage
	}
	return nil
}

type History struct {
	Version     int          `json:"version"`
	BaseID      string       `json:"base_id"`
	Base        Record       `json:"base"`
	Corrections []Correction `json:"corrections"`
	Current     Record       `json:"current"`
}

func (h History) Validate() error {
	if h.Version != 1 || h.Base.Validate() != nil || h.BaseID != h.Base.ID || len(h.Corrections) > 100 || h.Current.Validate() != nil {
		return ErrUsage
	}
	prior := h.Base
	for _, correction := range h.Corrections {
		if correction.BaseID != h.BaseID || correction.Validate(prior) != nil {
			return ErrUsage
		}
		prior = correction.Record
	}
	if !SameRecord(prior, h.Current) {
		return ErrUsage
	}
	return nil
}
