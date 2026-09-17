package webui

import (
	"encoding/hex"
	"math"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
)

const (
	MaxInspectionItems    = 100
	MaxInspectionModels   = 256
	MaxRouteCandidates    = 256
	MaxHealthChecks       = 512
	MaxInspectionReasons  = 16
	MaxInspectionFindings = 64
	MaxInspectionText     = 4096
)

type Availability string

const (
	Available   Availability = "available"
	Unavailable Availability = "unavailable"
)

func validAvailability(value Availability) bool { return value == Available || value == Unavailable }

type ModelInspection struct {
	ID            string     `json:"id"`
	Provider      string     `json:"provider"`
	Model         string     `json:"model"`
	Locality      string     `json:"locality"`
	Configured    bool       `json:"configured"`
	Enabled       bool       `json:"enabled"`
	Installed     bool       `json:"installed"`
	Usable        bool       `json:"usable"`
	Capabilities  []string   `json:"capabilities"`
	ContextTokens *int64     `json:"context_tokens,omitempty"`
	EstimatedCost *float64   `json:"estimated_cost,omitempty"`
	RAMBytes      *uint64    `json:"ram_bytes,omitempty"`
	VRAMBytes     *uint64    `json:"vram_bytes,omitempty"`
	SizeBytes     *uint64    `json:"size_bytes,omitempty"`
	Digest        string     `json:"digest,omitempty"`
	Family        string     `json:"family,omitempty"`
	ParameterSize string     `json:"parameter_size,omitempty"`
	Quantization  string     `json:"quantization,omitempty"`
	ModifiedAt    *time.Time `json:"modified_at,omitempty"`
	FailureDomain string     `json:"failure_domain,omitempty"`
	Health        string     `json:"health"`
	StatusCode    string     `json:"status_code,omitempty"`
}

func (m ModelInspection) Validate() error {
	if !optionalModelID(m.ID) || m.ID == "" || !boundedPrintable(m.Provider, 1, 128) ||
		!boundedPrintable(m.Model, 1, 512) || (m.Locality != "local" && m.Locality != "cloud") ||
		m.Capabilities == nil || len(m.Capabilities) > 128 || !boundedPrintable(m.FailureDomain, 0, 128) ||
		!boundedPrintable(m.Digest, 0, 64) || !boundedPrintable(m.Family, 0, 128) || !boundedPrintable(m.ParameterSize, 0, 128) ||
		!boundedPrintable(m.Quantization, 0, 128) || !boundedPrintable(m.StatusCode, 0, 128) ||
		m.Digest != "" && !validInspectionDigest(m.Digest, true) || m.ModifiedAt != nil && !validBrowserTime(*m.ModifiedAt) ||
		m.Usable && (!m.Configured || !m.Enabled || m.Health != "healthy") || m.Enabled && !m.Configured ||
		m.Installed && m.Locality != "local" || m.Locality == "cloud" && (m.Installed || m.SizeBytes != nil || m.Digest != "" || m.ModifiedAt != nil) {
		return ErrContract
	}
	if m.ContextTokens != nil && *m.ContextTokens < 0 || m.EstimatedCost != nil && !finiteNonnegative(*m.EstimatedCost) {
		return ErrContract
	}
	switch m.Health {
	case "healthy", "degraded", "unavailable", "disabled", "unknown":
	default:
		return ErrContract
	}
	seen := map[string]bool{}
	for _, capability := range m.Capabilities {
		if !boundedPrintable(capability, 1, 128) || seen[capability] {
			return ErrContract
		}
		seen[capability] = true
	}
	return nil
}

type ModelInspectionPage struct {
	Version               int               `json:"version"`
	Availability          Availability      `json:"availability"`
	ConfigID              string            `json:"config_id,omitempty"`
	RefreshedAt           *time.Time        `json:"refreshed_at,omitempty"`
	LocalTotalBytes       *uint64           `json:"local_total_bytes,omitempty"`
	LocalTotalKind        string            `json:"local_total_kind,omitempty"`
	LocalUnknownSizeCount int               `json:"local_unknown_size_count"`
	Models                []ModelInspection `json:"models"`
}

func (p ModelInspectionPage) Validate() error {
	if p.Version != ContractVersion || !validAvailability(p.Availability) || p.Models == nil || len(p.Models) > MaxInspectionModels ||
		(p.Availability == Available) != (p.ConfigID != "") || !validInspectionDigest(p.ConfigID, p.Availability == Available) ||
		p.LocalUnknownSizeCount < 0 || p.LocalUnknownSizeCount > MaxInspectionModels ||
		p.Availability == Available && (p.RefreshedAt == nil || !validBrowserTime(*p.RefreshedAt) || p.LocalTotalBytes == nil || p.LocalTotalKind != "logical_deduplicated") ||
		p.Availability == Unavailable && (len(p.Models) != 0 || p.RefreshedAt != nil || p.LocalTotalBytes != nil || p.LocalTotalKind != "" || p.LocalUnknownSizeCount != 0) {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, model := range p.Models {
		if model.Validate() != nil || seen[model.ID] {
			return ErrContract
		}
		seen[model.ID] = true
	}
	return encodedWithin(p, 256<<10)
}

type RouteCandidateInspection struct {
	Model           string   `json:"model"`
	Provider        string   `json:"provider"`
	FailureDomain   string   `json:"failure_domain,omitempty"`
	Disposition     string   `json:"disposition"`
	Score           *float64 `json:"score,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	Samples         *int64   `json:"samples,omitempty"`
	ConstraintCodes []string `json:"constraint_codes"`
}

func (c RouteCandidateInspection) Validate() error {
	if !boundedPrintable(c.Model, 1, 512) || !boundedPrintable(c.Provider, 1, 128) || !boundedPrintable(c.FailureDomain, 0, 128) ||
		c.ConstraintCodes == nil || len(c.ConstraintCodes) > MaxInspectionReasons || c.Score != nil && (!finite(*c.Score) || *c.Score < 0 || *c.Score > 1) ||
		c.Confidence != nil && (!finite(*c.Confidence) || *c.Confidence < 0 || *c.Confidence > 1) || c.Samples != nil && *c.Samples < 0 {
		return ErrContract
	}
	switch c.Disposition {
	case "selected", "fallback", "eligible":
		if c.Score == nil || c.Confidence == nil || c.Samples == nil || len(c.ConstraintCodes) != 0 {
			return ErrContract
		}
	case "excluded":
		if c.Score != nil || c.Confidence != nil || c.Samples != nil || len(c.ConstraintCodes) == 0 {
			return ErrContract
		}
	default:
		return ErrContract
	}
	seen := map[string]bool{}
	for _, reason := range c.ConstraintCodes {
		if !boundedPrintable(reason, 1, 64) || seen[reason] {
			return ErrContract
		}
		seen[reason] = true
	}
	return nil
}

type UsageTotalInspection struct {
	Records             int64    `json:"records"`
	KnownUsageRecords   int64    `json:"known_usage_records"`
	UnknownUsageRecords int64    `json:"unknown_usage_records"`
	InputTokens         *int64   `json:"input_tokens,omitempty"`
	OutputTokens        *int64   `json:"output_tokens,omitempty"`
	KnownCostRecords    int64    `json:"known_cost_records"`
	UnknownCostRecords  int64    `json:"unknown_cost_records"`
	NormalizedCost      *float64 `json:"normalized_cost,omitempty"`
}

func (t UsageTotalInspection) Validate() error {
	if t.Records < 0 || t.KnownUsageRecords < 0 || t.UnknownUsageRecords < 0 || t.KnownUsageRecords+t.UnknownUsageRecords != t.Records ||
		t.KnownCostRecords < 0 || t.UnknownCostRecords < 0 || t.KnownCostRecords+t.UnknownCostRecords != t.Records ||
		(t.InputTokens == nil) != (t.OutputTokens == nil) || t.InputTokens != nil && (*t.InputTokens < 0 || *t.OutputTokens < 0) ||
		t.NormalizedCost != nil && !finiteNonnegative(*t.NormalizedCost) {
		return ErrContract
	}
	if (t.Records > 0 && t.UnknownUsageRecords == 0) != (t.InputTokens != nil) ||
		(t.Records > 0 && t.UnknownCostRecords == 0) != (t.NormalizedCost != nil) {
		return ErrContract
	}
	return nil
}

type UsageInspection struct {
	Coverage          string               `json:"coverage"`
	UnaccountedRouted int64                `json:"unaccounted_routed_operations"`
	Primary           UsageTotalInspection `json:"primary"`
	Fallback          UsageTotalInspection `json:"fallback"`
	Classifier        UsageTotalInspection `json:"classifier"`
	Summarizer        UsageTotalInspection `json:"summarizer"`
	OrchestratorAudit UsageTotalInspection `json:"orchestrator_audit"`
	OptionalJudge     UsageTotalInspection `json:"optional_judge"`
	Routed            UsageTotalInspection `json:"routed"`
	Auxiliary         UsageTotalInspection `json:"auxiliary"`
	Overall           UsageTotalInspection `json:"overall"`
	CalculatedAt      time.Time            `json:"calculated_at"`
}

func (u UsageInspection) Validate() error {
	if u.UnaccountedRouted < 0 || !validBrowserTime(u.CalculatedAt) {
		return ErrContract
	}
	switch u.Coverage {
	case "complete":
		if u.UnaccountedRouted != 0 {
			return ErrContract
		}
	case "partial", "legacy_unavailable":
		if u.UnaccountedRouted == 0 {
			return ErrContract
		}
	default:
		return ErrContract
	}
	for _, total := range []UsageTotalInspection{u.Primary, u.Fallback, u.Classifier, u.Summarizer, u.OrchestratorAudit, u.OptionalJudge, u.Routed, u.Auxiliary, u.Overall} {
		if total.Validate() != nil {
			return ErrContract
		}
	}
	routed, ok := sumUsageTotals(u.Primary, u.Fallback)
	if !ok || !sameUsageTotal(routed, u.Routed) {
		return ErrContract
	}
	auxiliary, ok := sumUsageTotals(u.Classifier, u.Summarizer, u.OrchestratorAudit, u.OptionalJudge)
	if !ok || !sameUsageTotal(auxiliary, u.Auxiliary) {
		return ErrContract
	}
	overall, ok := sumUsageTotals(u.Routed, u.Auxiliary)
	if !ok || !sameUsageTotal(overall, u.Overall) {
		return ErrContract
	}
	return nil
}

func sumUsageTotals(values ...UsageTotalInspection) (UsageTotalInspection, bool) {
	var out UsageTotalInspection
	usageComplete, costComplete := true, true
	for _, value := range values {
		if value.Validate() != nil || addInspectionInt(&out.Records, value.Records) ||
			addInspectionInt(&out.KnownUsageRecords, value.KnownUsageRecords) || addInspectionInt(&out.UnknownUsageRecords, value.UnknownUsageRecords) ||
			addInspectionInt(&out.KnownCostRecords, value.KnownCostRecords) || addInspectionInt(&out.UnknownCostRecords, value.UnknownCostRecords) {
			return UsageTotalInspection{}, false
		}
		usageComplete = usageComplete && value.UnknownUsageRecords == 0
		costComplete = costComplete && value.UnknownCostRecords == 0
		if value.InputTokens != nil {
			if out.InputTokens == nil {
				input, output := int64(0), int64(0)
				out.InputTokens, out.OutputTokens = &input, &output
			}
			if addInspectionInt(out.InputTokens, *value.InputTokens) || addInspectionInt(out.OutputTokens, *value.OutputTokens) {
				return UsageTotalInspection{}, false
			}
		}
		if value.NormalizedCost != nil {
			if out.NormalizedCost == nil {
				cost := float64(0)
				out.NormalizedCost = &cost
			}
			*out.NormalizedCost += *value.NormalizedCost
			if !finiteNonnegative(*out.NormalizedCost) {
				return UsageTotalInspection{}, false
			}
		}
	}
	if out.Records == 0 || !usageComplete {
		out.InputTokens, out.OutputTokens = nil, nil
	}
	if out.Records == 0 || !costComplete {
		out.NormalizedCost = nil
	}
	return out, out.Validate() == nil
}

func addInspectionInt(target *int64, value int64) bool {
	if value < 0 || *target > math.MaxInt64-value {
		return true
	}
	*target += value
	return false
}

func sameUsageTotal(left, right UsageTotalInspection) bool {
	if left.Records != right.Records || left.KnownUsageRecords != right.KnownUsageRecords || left.UnknownUsageRecords != right.UnknownUsageRecords ||
		left.KnownCostRecords != right.KnownCostRecords || left.UnknownCostRecords != right.UnknownCostRecords {
		return false
	}
	return sameIntPointer(left.InputTokens, right.InputTokens) && sameIntPointer(left.OutputTokens, right.OutputTokens) &&
		sameFloatPointer(left.NormalizedCost, right.NormalizedCost)
}

func sameIntPointer(left, right *int64) bool {
	return (left == nil) == (right == nil) && (left == nil || *left == *right)
}

func sameFloatPointer(left, right *float64) bool {
	return (left == nil) == (right == nil) && (left == nil || *left == *right)
}

type TaskUsageInspection struct {
	Version      int              `json:"version"`
	TaskID       string           `json:"task_id"`
	Availability Availability     `json:"availability"`
	Usage        *UsageInspection `json:"usage,omitempty"`
}

func (u TaskUsageInspection) Validate() error {
	if u.Version != ContractVersion || !validID(u.TaskID) || !validAvailability(u.Availability) ||
		(u.Availability == Available) != (u.Usage != nil) {
		return ErrContract
	}
	if u.Usage != nil && u.Usage.Validate() != nil {
		return ErrContract
	}
	return encodedWithin(u, 32<<10)
}

type RouteInspection struct {
	Version      int                        `json:"version"`
	TaskID       string                     `json:"task_id"`
	Availability Availability               `json:"availability"`
	RouteID      string                     `json:"route_id,omitempty"`
	Domain       string                     `json:"domain,omitempty"`
	Profile      string                     `json:"profile,omitempty"`
	Explored     bool                       `json:"explored"`
	Candidates   []RouteCandidateInspection `json:"candidates"`
	Usage        *UsageInspection           `json:"usage,omitempty"`
}

func (r RouteInspection) Validate() error {
	if r.Version != ContractVersion || !validID(r.TaskID) || !validAvailability(r.Availability) || r.Candidates == nil ||
		len(r.Candidates) > MaxRouteCandidates || !boundedPrintable(r.RouteID, 0, 128) || !boundedPrintable(r.Domain, 0, 128) || !boundedPrintable(r.Profile, 0, 128) {
		return ErrContract
	}
	if r.Availability == Unavailable {
		if r.RouteID != "" || r.Domain != "" || r.Profile != "" || r.Explored || len(r.Candidates) != 0 || r.Usage != nil {
			return ErrContract
		}
		return nil
	}
	if r.RouteID == "" || r.Domain == "" || r.Profile == "" || len(r.Candidates) == 0 || r.Usage == nil || r.Usage.Validate() != nil {
		return ErrContract
	}
	selected := 0
	seen := map[string]bool{}
	for _, candidate := range r.Candidates {
		key := candidate.Provider + "\x00" + candidate.Model
		if candidate.Validate() != nil || seen[key] {
			return ErrContract
		}
		seen[key] = true
		if candidate.Disposition == "selected" {
			selected++
		}
	}
	if selected != 1 {
		return ErrContract
	}
	return encodedWithin(r, 256<<10)
}

type ToolInspection struct {
	CallID             string     `json:"call_id"`
	Name               string     `json:"name"`
	Behavior           string     `json:"behavior"`
	Permission         string     `json:"permission"`
	State              string     `json:"state"`
	Effect             string     `json:"effect"`
	Code               string     `json:"code,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	StartSequence      int64      `json:"start_sequence"`
	CompletionSequence *int64     `json:"completion_sequence,omitempty"`
}

func (t ToolInspection) Validate() error {
	if !validID(t.CallID) || !boundedPrintable(t.Name, 1, 128) || !boundedPrintable(t.Code, 0, 128) || !validBrowserTime(t.StartedAt) || t.StartSequence < 1 ||
		(t.CompletedAt == nil) != (t.CompletionSequence == nil) {
		return ErrContract
	}
	switch t.Behavior {
	case "read_only", "idempotent_write", "non_idempotent_write", "unknown":
	default:
		return ErrContract
	}
	switch t.Permission {
	case "not_required", "pending", "approved", "denied", "revoked", "unknown":
	default:
		return ErrContract
	}
	switch t.State {
	case "pending":
		if t.CompletedAt != nil || t.Effect != "uncertain" || t.Code != "" {
			return ErrContract
		}
	case "completed", "failed":
		if t.CompletedAt == nil || !validBrowserTime(*t.CompletedAt) || t.CompletedAt.Before(t.StartedAt) || *t.CompletionSequence <= t.StartSequence {
			return ErrContract
		}
		if t.Effect != "none" && t.Effect != "confirmed" && t.Effect != "uncertain" || t.State == "failed" && t.Code == "" || t.State == "completed" && t.Code != "" {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return nil
}

type ToolInspectionPage struct {
	Version    int              `json:"version"`
	TaskID     string           `json:"task_id"`
	Tools      []ToolInspection `json:"tools"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func (p ToolInspectionPage) Validate() error {
	if p.Version != ContractVersion || !validID(p.TaskID) || p.Tools == nil || len(p.Tools) > MaxInspectionItems || !validCursor(p.NextCursor) {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, tool := range p.Tools {
		if tool.Validate() != nil || seen[tool.CallID] {
			return ErrContract
		}
		seen[tool.CallID] = true
	}
	return encodedWithin(p, 128<<10)
}

type AuditFindingInspection struct {
	Summary      string   `json:"summary"`
	EvidenceRefs []string `json:"evidence_refs"`
}

func (f AuditFindingInspection) Validate() error {
	if !boundedPrintable(f.Summary, 1, MaxInspectionText) || f.EvidenceRefs == nil || len(f.EvidenceRefs) > 64 {
		return ErrContract
	}
	for _, ref := range f.EvidenceRefs {
		if !boundedPrintable(ref, 1, 256) {
			return ErrContract
		}
	}
	return nil
}

type AuditInspection struct {
	ID                 string                   `json:"id"`
	Status             string                   `json:"status"`
	ReviewerID         string                   `json:"reviewer_id"`
	EvaluatorModel     string                   `json:"evaluator_model"`
	EvaluatorProvider  string                   `json:"evaluator_provider"`
	RubricVersion      string                   `json:"rubric_version,omitempty"`
	Domain             string                   `json:"domain,omitempty"`
	Findings           []AuditFindingInspection `json:"findings"`
	EvidencePrecedence []string                 `json:"evidence_precedence"`
	Usage              *UsageTotalInspection    `json:"usage,omitempty"`
	StartedAt          time.Time                `json:"started_at"`
	FinishedAt         *time.Time               `json:"finished_at,omitempty"`
}

func (a AuditInspection) Validate() error {
	if !validID(a.ID) || !boundedPrintable(a.ReviewerID, 1, 128) || !boundedPrintable(a.EvaluatorModel, 1, 512) ||
		!boundedPrintable(a.EvaluatorProvider, 1, 128) || !boundedPrintable(a.RubricVersion, 0, 128) || !boundedPrintable(a.Domain, 0, 128) ||
		a.Findings == nil || len(a.Findings) > MaxInspectionFindings || a.EvidencePrecedence == nil || len(a.EvidencePrecedence) > 8 || !validBrowserTime(a.StartedAt) {
		return ErrContract
	}
	switch a.Status {
	case "pending":
		if a.FinishedAt != nil || a.RubricVersion != "" || a.Domain != "" || len(a.Findings) != 0 || a.Usage != nil {
			return ErrContract
		}
	case "completed", "rejected", "abstained":
		if a.FinishedAt == nil || !validBrowserTime(*a.FinishedAt) || a.FinishedAt.Before(a.StartedAt) || a.RubricVersion == "" || a.Domain == "" {
			return ErrContract
		}
	case "failed", "canceled":
		if a.FinishedAt == nil || !validBrowserTime(*a.FinishedAt) || a.FinishedAt.Before(a.StartedAt) || a.RubricVersion != "" || a.Domain != "" || len(a.Findings) != 0 || a.Usage != nil {
			return ErrContract
		}
	default:
		return ErrContract
	}
	for _, finding := range a.Findings {
		if finding.Validate() != nil {
			return ErrContract
		}
	}
	wantPrecedence := []string{"deterministic", "tool_result", "user_feedback", "llm_judge"}
	if len(a.EvidencePrecedence) != len(wantPrecedence) {
		return ErrContract
	}
	for index, source := range a.EvidencePrecedence {
		if source != wantPrecedence[index] {
			return ErrContract
		}
	}
	if a.Usage != nil && a.Usage.Validate() != nil {
		return ErrContract
	}
	return nil
}

type AuditInspectionPage struct {
	Version    int               `json:"version"`
	TaskID     string            `json:"task_id"`
	Audits     []AuditInspection `json:"audits"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func (p AuditInspectionPage) Validate() error {
	if p.Version != ContractVersion || !validID(p.TaskID) || p.Audits == nil || len(p.Audits) > MaxInspectionItems || !validCursor(p.NextCursor) {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, audit := range p.Audits {
		if audit.Validate() != nil || seen[audit.ID] {
			return ErrContract
		}
		seen[audit.ID] = true
	}
	return encodedWithin(p, 512<<10)
}

type HealthCheckInspection struct {
	Component string `json:"component"`
	ID        string `json:"id,omitempty"`
	Status    string `json:"status"`
	Code      string `json:"code"`
}

type HealthInspection struct {
	Version      int                     `json:"version"`
	Availability Availability            `json:"availability"`
	Status       string                  `json:"status"`
	Ready        *bool                   `json:"ready,omitempty"`
	CheckedAt    *time.Time              `json:"checked_at,omitempty"`
	Checks       []HealthCheckInspection `json:"checks"`
}

func (h HealthInspection) Validate() error {
	if h.Version != ContractVersion || !validAvailability(h.Availability) || h.Checks == nil || len(h.Checks) > MaxHealthChecks {
		return ErrContract
	}
	if h.Availability == Unavailable {
		if h.Status != "unavailable" || h.Ready != nil || h.CheckedAt != nil || len(h.Checks) != 0 {
			return ErrContract
		}
		return nil
	}
	if h.Ready == nil || h.CheckedAt == nil || !validBrowserTime(*h.CheckedAt) {
		return ErrContract
	}
	checks := make([]health.Check, len(h.Checks))
	for index, check := range h.Checks {
		checks[index] = health.Check{Component: check.Component, ID: check.ID, Status: check.Status, Code: check.Code}
	}
	report := health.Report{Version: 1, CheckedAt: *h.CheckedAt, Status: h.Status, Ready: *h.Ready, Checks: checks}
	if report.Validate() != nil {
		return ErrContract
	}
	return encodedWithin(h, 64<<10)
}

type ResourceInspection struct {
	Version         int          `json:"version"`
	Availability    Availability `json:"availability"`
	ObservedAt      *time.Time   `json:"observed_at,omitempty"`
	CPUs            *int         `json:"cpus,omitempty"`
	TotalRAM        *uint64      `json:"total_ram_bytes,omitempty"`
	AvailableRAM    *uint64      `json:"available_ram_bytes,omitempty"`
	SwapUsed        *uint64      `json:"swap_used_bytes,omitempty"`
	VRAMTotal       *uint64      `json:"vram_total_bytes,omitempty"`
	VRAMAvailable   *uint64      `json:"vram_available_bytes,omitempty"`
	UnifiedMemory   *bool        `json:"unified_memory,omitempty"`
	ThermalPressure *bool        `json:"thermal_pressure,omitempty"`
}

func (r ResourceInspection) Validate() error {
	if r.Version != ContractVersion || !validAvailability(r.Availability) {
		return ErrContract
	}
	if r.Availability == Unavailable {
		if r.ObservedAt != nil || r.CPUs != nil || r.TotalRAM != nil || r.AvailableRAM != nil || r.SwapUsed != nil || r.VRAMTotal != nil || r.VRAMAvailable != nil || r.UnifiedMemory != nil || r.ThermalPressure != nil {
			return ErrContract
		}
		return nil
	}
	if r.ObservedAt == nil || !validBrowserTime(*r.ObservedAt) || r.CPUs != nil && *r.CPUs < 1 ||
		(r.TotalRAM == nil) != (r.AvailableRAM == nil) || r.TotalRAM != nil && *r.AvailableRAM > *r.TotalRAM ||
		(r.VRAMTotal == nil) != (r.VRAMAvailable == nil) || r.VRAMTotal != nil && *r.VRAMAvailable > *r.VRAMTotal {
		return ErrContract
	}
	return encodedWithin(r, 4096)
}

func validCursor(value string) bool        { return value == "" || boundedPrintable(value, 1, MaxCursorBytes) }
func finite(value float64) bool            { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func finiteNonnegative(value float64) bool { return finite(value) && value >= 0 }

func validInspectionDigest(value string, required bool) bool {
	if !required {
		return value == ""
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && strings.ToLower(value) == value
}
