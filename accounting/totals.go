package accounting

import (
	"math"
	"time"
)

type Scope struct {
	TaskID    string `json:"task_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

func (s Scope) Validate() error {
	if (s.TaskID != "" && !label(s.TaskID, 128)) || (s.SessionID != "" && !label(s.SessionID, 128)) {
		return ErrUsage
	}
	return nil
}

type Total struct {
	Records             int64    `json:"records"`
	KnownUsageRecords   int64    `json:"known_usage_records"`
	UnknownUsageRecords int64    `json:"unknown_usage_records"`
	KnownInputTokens    int64    `json:"known_input_tokens"`
	KnownOutputTokens   int64    `json:"known_output_tokens"`
	InputTokens         *int64   `json:"input_tokens,omitempty"`
	OutputTokens        *int64   `json:"output_tokens,omitempty"`
	KnownCostRecords    int64    `json:"known_cost_records"`
	UnknownCostRecords  int64    `json:"unknown_cost_records"`
	KnownNormalizedCost float64  `json:"known_normalized_cost"`
	NormalizedCost      *float64 `json:"normalized_cost,omitempty"`
}

type Coverage string

const (
	CompleteCoverage          Coverage = "complete"
	PartialCoverage           Coverage = "partial"
	LegacyUnavailableCoverage Coverage = "legacy_unavailable"
)

func (t Total) Validate() error {
	usageRecords, usageOverflow := checkedAdd(t.KnownUsageRecords, t.UnknownUsageRecords)
	costRecords, costOverflow := checkedAdd(t.KnownCostRecords, t.UnknownCostRecords)
	if t.Records < 0 || usageOverflow || usageRecords != t.Records || t.KnownInputTokens < 0 || t.KnownOutputTokens < 0 || costOverflow || costRecords != t.Records || !number(t.KnownNormalizedCost) {
		return ErrUsage
	}
	if (t.InputTokens == nil) != (t.OutputTokens == nil) || (t.Records > 0 && t.UnknownUsageRecords == 0) != (t.InputTokens != nil) {
		return ErrUsage
	}
	if t.InputTokens != nil && (*t.InputTokens != t.KnownInputTokens || *t.OutputTokens != t.KnownOutputTokens) {
		return ErrUsage
	}
	if (t.Records > 0 && t.UnknownCostRecords == 0) != (t.NormalizedCost != nil) || (t.NormalizedCost != nil && *t.NormalizedCost != t.KnownNormalizedCost) {
		return ErrUsage
	}
	return nil
}

func nilIf(b bool) error {
	if b {
		return ErrUsage
	}
	return nil
}

type Totals struct {
	Version                     int       `json:"version"`
	Scope                       Scope     `json:"scope"`
	Coverage                    Coverage  `json:"coverage"`
	UnaccountedRoutedOperations int64     `json:"unaccounted_routed_operations"`
	Primary                     Total     `json:"primary"`
	Fallback                    Total     `json:"fallback"`
	Classifier                  Total     `json:"classifier"`
	Summarizer                  Total     `json:"summarizer"`
	OrchestratorAudit           Total     `json:"orchestrator_audit"`
	Judge                       Total     `json:"optional_judge"`
	Routed                      Total     `json:"routed"`
	Auxiliary                   Total     `json:"auxiliary"`
	Overall                     Total     `json:"overall"`
	CalculatedAt                time.Time `json:"calculated_at"`
}

func (t Totals) Validate() error {
	if t.Version != 1 || t.Scope.Validate() != nil || t.CalculatedAt.IsZero() || t.CalculatedAt.Location() != time.UTC || t.UnaccountedRoutedOperations < 0 {
		return ErrUsage
	}
	switch t.Coverage {
	case CompleteCoverage:
		if t.UnaccountedRoutedOperations != 0 {
			return ErrUsage
		}
	case PartialCoverage:
		if t.UnaccountedRoutedOperations == 0 || t.Overall.Records == 0 {
			return ErrUsage
		}
	case LegacyUnavailableCoverage:
		if t.UnaccountedRoutedOperations == 0 || t.Overall.Records != 0 {
			return ErrUsage
		}
	default:
		return ErrUsage
	}
	for _, total := range []Total{t.Primary, t.Fallback, t.Classifier, t.Summarizer, t.OrchestratorAudit, t.Judge, t.Routed, t.Auxiliary, t.Overall} {
		if total.Validate() != nil {
			return ErrUsage
		}
	}
	routed, err := Sum(t.Primary, t.Fallback)
	if err != nil || !sameTotal(routed, t.Routed) {
		return ErrUsage
	}
	auxiliary, err := Sum(t.Classifier, t.Summarizer, t.OrchestratorAudit, t.Judge)
	if err != nil || !sameTotal(auxiliary, t.Auxiliary) {
		return ErrUsage
	}
	overall, err := Sum(t.Routed, t.Auxiliary)
	if err != nil || !sameTotal(overall, t.Overall) {
		return ErrUsage
	}
	return nil
}

func TotalFor(r Record) (Total, error) {
	if r.Validate() != nil {
		return Total{}, ErrUsage
	}
	t := Total{Records: 1}
	if r.Usage == nil {
		t.UnknownUsageRecords = 1
	} else {
		t.KnownUsageRecords, t.KnownInputTokens, t.KnownOutputTokens = 1, r.Usage.InputTokens, r.Usage.OutputTokens
		t.InputTokens, t.OutputTokens = int64Pointer(t.KnownInputTokens), int64Pointer(t.KnownOutputTokens)
	}
	if r.NormalizedCost == nil {
		t.UnknownCostRecords = 1
	} else {
		t.KnownCostRecords, t.KnownNormalizedCost = 1, *r.NormalizedCost
		t.NormalizedCost = float64Pointer(t.KnownNormalizedCost)
	}
	return t, t.Validate()
}

func Sum(values ...Total) (Total, error) {
	var out Total
	for _, value := range values {
		if value.Validate() != nil || addOverflow(&out.Records, value.Records) || addOverflow(&out.KnownUsageRecords, value.KnownUsageRecords) || addOverflow(&out.UnknownUsageRecords, value.UnknownUsageRecords) || addOverflow(&out.KnownInputTokens, value.KnownInputTokens) || addOverflow(&out.KnownOutputTokens, value.KnownOutputTokens) || addOverflow(&out.KnownCostRecords, value.KnownCostRecords) || addOverflow(&out.UnknownCostRecords, value.UnknownCostRecords) {
			return Total{}, ErrUsage
		}
		out.KnownNormalizedCost += value.KnownNormalizedCost
		if !number(out.KnownNormalizedCost) {
			return Total{}, ErrUsage
		}
	}
	if out.Records > 0 && out.UnknownUsageRecords == 0 {
		out.InputTokens, out.OutputTokens = int64Pointer(out.KnownInputTokens), int64Pointer(out.KnownOutputTokens)
	}
	if out.Records > 0 && out.UnknownCostRecords == 0 {
		out.NormalizedCost = float64Pointer(out.KnownNormalizedCost)
	}
	return out, out.Validate()
}

func int64Pointer(value int64) *int64       { return &value }
func float64Pointer(value float64) *float64 { return &value }

func sameTotal(a, b Total) bool {
	if a.Records != b.Records || a.KnownUsageRecords != b.KnownUsageRecords || a.UnknownUsageRecords != b.UnknownUsageRecords || a.KnownInputTokens != b.KnownInputTokens || a.KnownOutputTokens != b.KnownOutputTokens || a.KnownCostRecords != b.KnownCostRecords || a.UnknownCostRecords != b.UnknownCostRecords || a.KnownNormalizedCost != b.KnownNormalizedCost {
		return false
	}
	if (a.InputTokens == nil) != (b.InputTokens == nil) || (a.OutputTokens == nil) != (b.OutputTokens == nil) || (a.NormalizedCost == nil) != (b.NormalizedCost == nil) {
		return false
	}
	return (a.InputTokens == nil || *a.InputTokens == *b.InputTokens) && (a.OutputTokens == nil || *a.OutputTokens == *b.OutputTokens) && (a.NormalizedCost == nil || *a.NormalizedCost == *b.NormalizedCost)
}

func addOverflow(target *int64, value int64) bool {
	if value < 0 || *target > math.MaxInt64-value {
		return true
	}
	*target += value
	return false
}

func checkedAdd(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, true
	}
	return a + b, false
}
