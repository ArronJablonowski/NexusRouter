// Package accounting defines provider-neutral, durable usage and cost facts.
// It contains no storage or routing policy and never treats an estimate as a
// measured charge.
package accounting

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

var (
	ErrUsage     = errors.New("invalid usage accounting record")
	ErrSensitive = errors.New("usage accounting identity contains a current secret")
)

type Role string

const (
	PrimaryExecution  Role = "primary_execution"
	Fallback          Role = "fallback"
	Classifier        Role = "classifier"
	Summarizer        Role = "summarizer"
	OrchestratorAudit Role = "orchestrator_audit"
	OptionalJudge     Role = "optional_judge"
)

func (r Role) Valid() bool {
	switch r {
	case PrimaryExecution, Fallback, Classifier, Summarizer, OrchestratorAudit, OptionalJudge:
		return true
	}
	return false
}

func (r Role) Auxiliary() bool {
	return r == Classifier || r == Summarizer || r == OrchestratorAudit || r == OptionalJudge
}

type EvidenceKind string

const (
	EventEvidence   EvidenceKind = "event"
	ReviewEvidence  EvidenceKind = "review"
	AuditEvidence   EvidenceKind = "audit"
	SummaryEvidence EvidenceKind = "summary"
)

type Disposition string

const (
	Completed Disposition = "completed"
	Failed    Disposition = "failed"
	Canceled  Disposition = "canceled"
)

type RetryClass string

const (
	NotApplicable RetryClass = "not_applicable"
	Retryable     RetryClass = "retryable"
	NonRetryable  RetryClass = "non_retryable"
	Uncertain     RetryClass = "uncertain"
)

type PricingMethod string

const (
	TokenRates       PricingMethod = "token_rates"
	FlatRate         PricingMethod = "flat_rate"
	ProviderReported PricingMethod = "provider_reported"
)

type PricingBasis string

const (
	ConfiguredEstimate    PricingBasis = "configured_estimate"
	ProviderReportedBasis PricingBasis = "provider_reported"
	ProviderReconciled    PricingBasis = "provider_reconciled"
	OperatorReconciled    PricingBasis = "operator_reconciled"
)

func (b PricingBasis) Valid() bool {
	return b == ConfiguredEstimate || b == ProviderReportedBasis || b == ProviderReconciled || b == OperatorReconciled
}

// PricingProvenance is a versioned snapshot of the rule used to normalize a
// charge to USD. Source is an opaque catalog/invoice identity, never a secret
// or raw provider response. Unit rates are USD per token.
type PricingProvenance struct {
	Version        int           `json:"version"`
	ID             string        `json:"id"`
	Source         string        `json:"source"`
	Digest         string        `json:"digest"`
	Currency       string        `json:"currency"`
	Method         PricingMethod `json:"method"`
	Basis          PricingBasis  `json:"basis"`
	InputUnitCost  float64       `json:"input_unit_cost"`
	OutputUnitCost float64       `json:"output_unit_cost"`
	FixedCost      float64       `json:"fixed_cost"`
	EffectiveAt    time.Time     `json:"effective_at"`
}

func (p PricingProvenance) Validate(usage *providers.Usage, cost float64) error {
	if p.Version != 1 || !label(p.ID, 128) || !label(p.Source, 256) || !digest(p.Digest) || p.Currency != "USD" || !p.Basis.Valid() || p.EffectiveAt.IsZero() || p.EffectiveAt.Location() != time.UTC || !number(cost) || !number(p.InputUnitCost) || !number(p.OutputUnitCost) || !number(p.FixedCost) {
		return ErrUsage
	}
	switch p.Method {
	case TokenRates:
		if usage == nil {
			return ErrUsage
		}
		want := p.FixedCost + float64(usage.InputTokens)*p.InputUnitCost + float64(usage.OutputTokens)*p.OutputUnitCost
		if !number(want) || !closeCost(want, cost) {
			return ErrUsage
		}
	case FlatRate, ProviderReported:
		if p.InputUnitCost != 0 || p.OutputUnitCost != 0 || !closeCost(p.FixedCost, cost) {
			return ErrUsage
		}
	default:
		return ErrUsage
	}
	if p.Method == ProviderReported && p.Basis == ConfiguredEstimate {
		return ErrUsage
	}
	return nil
}

// Record is one immutable operation-level accounting fact. RouteID names
// a durable routing/admission record; EvidenceID names the durable result that
// proves attribution. Missing provider usage is explicit as Usage=nil, never
// silently converted to zero tokens or zero cost.
type Record struct {
	Version            int                `json:"version"`
	ID                 string             `json:"id"`
	TaskID             string             `json:"task_id"`
	SessionID          string             `json:"session_id"`
	OperationID        string             `json:"operation_id"`
	CandidateAttemptID string             `json:"candidate_attempt_id,omitempty"`
	RouteID            string             `json:"route_id"`
	EvidenceID         string             `json:"evidence_id"`
	AuditID            string             `json:"audit_id,omitempty"`
	Provider           string             `json:"provider"`
	Model              string             `json:"model"`
	Role               Role               `json:"role"`
	EvidenceKind       EvidenceKind       `json:"evidence_kind"`
	Usage              *providers.Usage   `json:"usage,omitempty"`
	NormalizedCost     *float64           `json:"normalized_cost,omitempty"`
	Pricing            *PricingProvenance `json:"pricing,omitempty"`
	Disposition        Disposition        `json:"disposition"`
	RetryClass         RetryClass         `json:"retry_class"`
	OccurredAt         time.Time          `json:"occurred_at"`
}

func (r Record) Validate() error {
	if r.Version != 1 || !label(r.ID, 128) || !label(r.TaskID, 128) || !label(r.SessionID, 128) || !label(r.OperationID, 128) || (r.CandidateAttemptID != "" && !label(r.CandidateAttemptID, 128)) || !label(r.RouteID, 128) || !label(r.EvidenceID, 128) || !label(r.Provider, 128) || !label(r.Model, 512) || !r.Role.Valid() || r.OccurredAt.IsZero() || r.OccurredAt.Location() != time.UTC {
		return ErrUsage
	}
	if r.Usage != nil && (r.Usage.InputTokens < 0 || r.Usage.OutputTokens < 0) {
		return ErrUsage
	}
	if (r.NormalizedCost == nil) != (r.Pricing == nil) {
		return ErrUsage
	}
	if r.NormalizedCost != nil && (r.Pricing.Validate(r.Usage, *r.NormalizedCost) != nil) {
		return ErrUsage
	}
	switch r.Disposition {
	case Completed:
		if r.RetryClass != NotApplicable {
			return ErrUsage
		}
	case Failed:
		if r.RetryClass != Retryable && r.RetryClass != NonRetryable && r.RetryClass != Uncertain {
			return ErrUsage
		}
	case Canceled:
		if r.RetryClass != NonRetryable {
			return ErrUsage
		}
	default:
		return ErrUsage
	}
	switch r.Role {
	case PrimaryExecution, Fallback, Classifier:
		if r.EvidenceKind != EventEvidence || r.AuditID != "" {
			return ErrUsage
		}
	case Summarizer:
		if r.EvidenceKind != SummaryEvidence || r.AuditID != "" {
			return ErrUsage
		}
	case OrchestratorAudit, OptionalJudge:
		if r.Disposition == Completed && (r.EvidenceKind != AuditEvidence || r.AuditID != r.EvidenceID) {
			return ErrUsage
		}
		if r.Disposition != Completed && (r.EvidenceKind != ReviewEvidence || r.AuditID != "") {
			return ErrUsage
		}
	default:
		return ErrUsage
	}
	return nil
}

func SameUsage(a, b *providers.Usage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func label(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

func digest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}

func number(v float64) bool { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

func closeCost(a, b float64) bool {
	delta := math.Abs(a - b)
	return delta <= math.Max(1e-12, math.Max(math.Abs(a), math.Abs(b))*1e-9)
}

// SafeRecord applies the same current-secret fail-closed rule expected of
// public inspection layers. Records intentionally contain no free-form text
// that could be partially redacted into a misleading accounting identity.
func SafeRecord(r Record, secrets []string) (Record, error) {
	if r.Validate() != nil {
		return Record{}, ErrUsage
	}
	values := []string{r.ID, r.TaskID, r.SessionID, r.OperationID, r.CandidateAttemptID, r.RouteID, r.EvidenceID, r.AuditID, r.Provider, r.Model}
	if r.Pricing != nil {
		values = append(values, r.Pricing.ID, r.Pricing.Source, r.Pricing.Digest, r.Pricing.Currency, string(r.Pricing.Method))
	}
	for _, value := range values {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(value, secret) {
				return Record{}, ErrSensitive
			}
		}
	}
	return CloneRecord(r), nil
}

func CloneRecord(r Record) Record {
	if r.Usage != nil {
		u := *r.Usage
		r.Usage = &u
	}
	if r.NormalizedCost != nil {
		c := *r.NormalizedCost
		r.NormalizedCost = &c
	}
	if r.Pricing != nil {
		p := *r.Pricing
		r.Pricing = &p
	}
	return r
}

func SameRecord(a, b Record) bool {
	if a.Version != b.Version || a.ID != b.ID || a.TaskID != b.TaskID || a.SessionID != b.SessionID || a.OperationID != b.OperationID || a.CandidateAttemptID != b.CandidateAttemptID || a.RouteID != b.RouteID || a.EvidenceID != b.EvidenceID || a.AuditID != b.AuditID || a.Provider != b.Provider || a.Model != b.Model || a.Role != b.Role || a.EvidenceKind != b.EvidenceKind || !SameUsage(a.Usage, b.Usage) || a.Disposition != b.Disposition || a.RetryClass != b.RetryClass || !a.OccurredAt.Equal(b.OccurredAt) {
		return false
	}
	if (a.NormalizedCost == nil) != (b.NormalizedCost == nil) || (a.Pricing == nil) != (b.Pricing == nil) {
		return false
	}
	if a.NormalizedCost != nil && *a.NormalizedCost != *b.NormalizedCost {
		return false
	}
	return a.Pricing == nil || *a.Pricing == *b.Pricing
}
