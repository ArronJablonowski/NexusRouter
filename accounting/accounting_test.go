package accounting

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func validRecord(role Role) Record {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	usage, cost := &providers.Usage{InputTokens: 10, OutputTokens: 4}, 0.024
	r := Record{Version: 1, ID: "usage-1", TaskID: "task-1", SessionID: "session-1", OperationID: "task-1", CandidateAttemptID: "attempt-1", RouteID: "route-1", EvidenceID: "event-1", Provider: "local", Model: "model:latest", Role: role, EvidenceKind: EventEvidence, Usage: usage, NormalizedCost: &cost, Pricing: &PricingProvenance{Version: 1, ID: "prices-1", Source: "catalog-2026-09", Digest: strings.Repeat("a", 64), Currency: "USD", Method: TokenRates, Basis: ConfiguredEstimate, InputUnitCost: .001, OutputUnitCost: .002, FixedCost: .006, EffectiveAt: now}, Disposition: Completed, RetryClass: NotApplicable, OccurredAt: now}
	switch role {
	case Summarizer:
		r.OperationID, r.RouteID, r.EvidenceID, r.EvidenceKind = "summary-1", "summary-1", "summary-1", SummaryEvidence
	case OrchestratorAudit, OptionalJudge:
		r.OperationID, r.RouteID, r.EvidenceID, r.AuditID, r.EvidenceKind = "review-1", "review-1", "audit-1", "audit-1", AuditEvidence
	}
	return r
}

func TestRecordRolesJSONAndMissingUsage(t *testing.T) {
	for _, role := range []Role{PrimaryExecution, Fallback, Classifier, Summarizer, OrchestratorAudit, OptionalJudge} {
		r := validRecord(role)
		if err := r.Validate(); err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		body, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{`"task_id"`, `"session_id"`, `"operation_id"`, `"route_id"`, `"provider"`, `"model"`, `"source"`, `"currency"`, `"basis"`, `"output_unit_cost"`, `"fixed_cost"`} {
			if !strings.Contains(string(body), field) {
				t.Fatalf("%s missing from %s", field, body)
			}
		}
		var got Record
		if json.Unmarshal(body, &got) != nil || !SameRecord(r, got) {
			t.Fatalf("round trip mismatch: %#v", got)
		}
	}
	r := validRecord(PrimaryExecution)
	r.Usage, r.NormalizedCost, r.Pricing = nil, nil, nil
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	zero := providers.Usage{}
	r.Usage = &zero
	if SameUsage(nil, r.Usage) || r.Validate() != nil {
		t.Fatal("missing usage must remain distinct from measured zero")
	}
}

func TestAuditFailureAndPricingBasis(t *testing.T) {
	r := validRecord(OrchestratorAudit)
	r.Disposition, r.RetryClass, r.EvidenceKind, r.EvidenceID, r.AuditID = Failed, Retryable, ReviewEvidence, "review-1", ""
	r.Usage, r.NormalizedCost, r.Pricing = nil, nil, nil
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.AuditID = "audit-1"
	if !errors.Is(r.Validate(), ErrUsage) {
		t.Fatal("failed review cannot claim audit evidence")
	}
	r = validRecord(PrimaryExecution)
	r.Pricing.Method, r.Pricing.Basis = ProviderReported, ConfiguredEstimate
	r.Pricing.InputUnitCost, r.Pricing.OutputUnitCost, r.Pricing.FixedCost = 0, 0, *r.NormalizedCost
	if !errors.Is(r.Validate(), ErrUsage) {
		t.Fatal("provider-reported method cannot disguise an estimate")
	}
}

func TestCorrectionHistoryRequiresExactCurrent(t *testing.T) {
	base := validRecord(PrimaryExecution)
	cost := .030
	next := CloneRecord(base)
	next.ID, next.NormalizedCost = "usage-2", &cost
	next.Pricing = &PricingProvenance{Version: 1, ID: "invoice-1", Source: "provider-invoice", Digest: strings.Repeat("b", 64), Currency: "USD", Method: ProviderReported, Basis: ProviderReconciled, FixedCost: cost, EffectiveAt: base.OccurredAt}
	c := Correction{Version: 1, ID: next.ID, BaseID: base.ID, Supersedes: base.ID, Evidence: "invoice-line-1", Reason: ProviderReconciliation, Record: next, RecordedAt: base.OccurredAt.Add(time.Second)}
	h := History{Version: 1, BaseID: base.ID, Base: base, Corrections: []Correction{c}, Current: CloneRecord(next)}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	h.Current.Provider = "other"
	if !errors.Is(h.Validate(), ErrUsage) {
		t.Fatal("current record must equal the correction head")
	}
}

func TestTotalsReconcileAndRejectOverflow(t *testing.T) {
	a, _ := TotalFor(validRecord(PrimaryExecution))
	b := validRecord(Summarizer)
	b.Usage, b.NormalizedCost, b.Pricing = nil, nil, nil
	aux, _ := TotalFor(b)
	zero := Total{}
	totals := Totals{Version: 1, Scope: Scope{}, Coverage: CompleteCoverage, Primary: a, Fallback: zero, Classifier: zero, Summarizer: aux, OrchestratorAudit: zero, Judge: zero, Routed: a, Auxiliary: aux, CalculatedAt: time.Now().UTC()}
	totals.Overall, _ = Sum(a, aux)
	if err := totals.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := Total{Records: math.MaxInt64, KnownUsageRecords: math.MaxInt64, UnknownUsageRecords: 1, KnownCostRecords: math.MaxInt64, UnknownCostRecords: 1}
	if !errors.Is(bad.Validate(), ErrUsage) {
		t.Fatal("count reconciliation overflow accepted")
	}
	if _, err := Sum(Total{}, Total{Records: math.MaxInt64, KnownUsageRecords: math.MaxInt64, KnownCostRecords: math.MaxInt64}, Total{Records: 1, KnownUsageRecords: 1, KnownCostRecords: 1}); !errors.Is(err, ErrUsage) {
		t.Fatal("sum overflow accepted")
	}
	body, _ := json.Marshal(totals)
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 14 || reflect.DeepEqual(fields["primary"], fields["fallback"]) {
		t.Fatalf("ambiguous totals JSON: %s", body)
	}
}

func TestSafeRecordUsesCurrentSecrets(t *testing.T) {
	r := validRecord(PrimaryExecution)
	r.Model = "model-secret-current"
	if _, err := SafeRecord(r, []string{"secret-current"}); !errors.Is(err, ErrSensitive) {
		t.Fatal("current secret was exposed")
	}
	r.Model = "model-safe"
	safe, err := SafeRecord(r, []string{"rotated-secret"})
	if err != nil || !SameRecord(r, safe) {
		t.Fatalf("safe projection failed: %v", err)
	}
}
