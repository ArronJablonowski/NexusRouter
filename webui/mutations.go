package webui

import (
	"time"
)

const (
	MaxApprovalPromptBytes = 16 << 10
	MaxApprovalScopeBytes  = 4 << 10
	MaxApprovalItems       = 100
	MaxEvidenceSummaries   = 32
	MaxOperationItems      = 100
)

// ChatMutationReceipt is the content-free durable acknowledgement returned by
// submit and follow-up operations. Provider output, usage, configuration and
// lease details are intentionally not representable.
type ChatMutationReceipt struct {
	Version      int    `json:"version"`
	OperationID  string `json:"operation_id"`
	SubmissionID string `json:"submission_id"`
	State        string `json:"state"`
	ChatID       string `json:"chat_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	Revision     *int64 `json:"revision,omitempty"`
}

func (r ChatMutationReceipt) Validate() error {
	if r.Version != ContractVersion || !validID(r.OperationID) || !validID(r.SubmissionID) ||
		!optionalID(r.ChatID) || !optionalID(r.TaskID) || !optionalRevision(r.Revision) ||
		(r.ChatID == "") != (r.TaskID == "") || (r.Revision != nil) != (r.TaskID != "") {
		return ErrContract
	}
	switch r.State {
	case "queued":
	case "running", "completed":
		if r.TaskID == "" {
			return ErrContract
		}
	case "failed", "canceled":
	default:
		return ErrContract
	}
	if r.Revision != nil && *r.Revision < 1 {
		return ErrContract
	}
	return encodedWithin(r, 4096)
}

type CancellationReceipt struct {
	Version     int        `json:"version"`
	OperationID string     `json:"operation_id"`
	TargetKind  string     `json:"target_kind"`
	TargetID    string     `json:"target_id"`
	State       string     `json:"state"`
	Requested   bool       `json:"requested"`
	RequestID   string     `json:"request_id,omitempty"`
	RequestedAt *time.Time `json:"requested_at,omitempty"`
	Revision    *int64     `json:"revision,omitempty"`
}

func (r CancellationReceipt) Validate() error {
	if r.Version != ContractVersion || !validID(r.OperationID) || !validID(r.TargetID) ||
		!optionalID(r.RequestID) || !optionalRevision(r.Revision) {
		return ErrContract
	}
	switch r.State {
	case "queued", "running", "completed", "failed", "canceled":
	default:
		return ErrContract
	}
	switch r.TargetKind {
	case "task":
		if r.Revision == nil || *r.Revision < 1 || r.State == "queued" ||
			r.Requested != (r.RequestID != "") || r.Requested != (r.RequestedAt != nil) {
			return ErrContract
		}
	case "submission":
		if r.Revision != nil || r.RequestID != "" || r.RequestedAt != nil {
			return ErrContract
		}
	default:
		return ErrContract
	}
	if r.RequestedAt != nil && !validBrowserTime(*r.RequestedAt) {
		return ErrContract
	}
	return encodedWithin(r, 4096)
}

type SteeringReceipt struct {
	Version         int       `json:"version"`
	OperationID     string    `json:"operation_id"`
	ID              string    `json:"id"`
	TaskID          string    `json:"task_id"`
	State           string    `json:"state"`
	CreatedAt       time.Time `json:"created_at"`
	AppliedRevision *int64    `json:"applied_revision,omitempty"`
}

func (r SteeringReceipt) Validate() error {
	if r.Version != ContractVersion || !validID(r.OperationID) || !validID(r.ID) || !validID(r.TaskID) ||
		!validBrowserTime(r.CreatedAt) || !optionalRevision(r.AppliedRevision) {
		return ErrContract
	}
	if r.State == "pending" && r.AppliedRevision == nil {
		return encodedWithin(r, 4096)
	}
	if r.State == "applied" && r.AppliedRevision != nil && *r.AppliedRevision >= 1 {
		return encodedWithin(r, 4096)
	}
	return ErrContract
}

// TaskControlStatus is an authority-free projection. Every true capability is
// advisory; the mutation service must still re-read and compare the revision.
type TaskControlStatus struct {
	Version     int    `json:"version"`
	TaskID      string `json:"task_id"`
	Revision    int64  `json:"revision"`
	CanResume   bool   `json:"can_resume"`
	CanSteer    bool   `json:"can_steer"`
	CanCancel   bool   `json:"can_cancel"`
	CanFeedback bool   `json:"can_feedback"`
}

func (s TaskControlStatus) Validate() error {
	if s.Version != ContractVersion || !validID(s.TaskID) || s.Revision < 1 {
		return ErrContract
	}
	return encodedWithin(s, 2048)
}

type EvidenceClass string

const (
	ObjectiveEvidence  EvidenceClass = "objective"
	SubjectiveEvidence EvidenceClass = "subjective"
	AdvisoryEvidence   EvidenceClass = "advisory"
)

type EvidenceOutcome string

const (
	EvidenceAccepted  EvidenceOutcome = "accepted"
	EvidenceRejected  EvidenceOutcome = "rejected"
	EvidenceAbstained EvidenceOutcome = "abstained"
)

// EvidenceSummary deliberately excludes evaluator references, model/provider
// identity, costs, timings and raw evidence.
type EvidenceSummary struct {
	Class          EvidenceClass   `json:"class"`
	Source         string          `json:"source"`
	Outcome        EvidenceOutcome `json:"outcome"`
	ReferenceCount int             `json:"reference_count"`
}

func (s EvidenceSummary) Validate() error {
	if s.ReferenceCount < 1 || s.ReferenceCount > 1024 ||
		(s.Outcome != EvidenceAccepted && s.Outcome != EvidenceRejected && s.Outcome != EvidenceAbstained) {
		return ErrContract
	}
	switch s.Source {
	case "deterministic", "tool_result":
		if s.Class != ObjectiveEvidence {
			return ErrContract
		}
	case "user_feedback":
		if s.Class != SubjectiveEvidence {
			return ErrContract
		}
	case "llm_judge":
		if s.Class != AdvisoryEvidence {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return nil
}

type FeedbackContext struct {
	Version         int               `json:"version"`
	TaskID          string            `json:"task_id"`
	Revision        int64             `json:"revision"`
	Objective       []EvidenceSummary `json:"objective"`
	Subjective      *EvidenceSummary  `json:"subjective,omitempty"`
	Advisory        *EvidenceSummary  `json:"advisory,omitempty"`
	FeedbackID      string            `json:"feedback_id,omitempty"`
	FeedbackAllowed bool              `json:"feedback_allowed"`
	DenialCode      string            `json:"denial_code,omitempty"`
}

func (c FeedbackContext) Validate() error {
	if c.Version != ContractVersion || !validID(c.TaskID) || c.Revision < 1 || c.Objective == nil ||
		len(c.Objective) > MaxEvidenceSummaries || !optionalID(c.FeedbackID) || !optionalID(c.DenialCode) ||
		(c.FeedbackID != "") != (c.Subjective != nil) || c.FeedbackAllowed == (c.DenialCode != "") {
		return ErrContract
	}
	for _, evidence := range c.Objective {
		if evidence.Validate() != nil || evidence.Class != ObjectiveEvidence {
			return ErrContract
		}
	}
	if c.Subjective != nil && (c.Subjective.Validate() != nil || c.Subjective.Class != SubjectiveEvidence) {
		return ErrContract
	}
	if c.Advisory != nil && (c.Advisory.Validate() != nil || c.Advisory.Class != AdvisoryEvidence) {
		return ErrContract
	}
	return encodedWithin(c, 16<<10)
}

type FeedbackReceipt struct {
	Version       int           `json:"version"`
	OperationID   string        `json:"operation_id"`
	TaskID        string        `json:"task_id"`
	FeedbackID    string        `json:"feedback_id"`
	Revision      int64         `json:"revision"`
	State         string        `json:"state"`
	Accepted      bool          `json:"accepted"`
	EvidenceClass EvidenceClass `json:"evidence_class"`
	Source        string        `json:"source"`
}

func (r FeedbackReceipt) Validate() error {
	if r.Version != ContractVersion || !validID(r.OperationID) || !validID(r.TaskID) || !validID(r.FeedbackID) ||
		r.Revision < 1 || (r.State != "recorded" && r.State != "revised") ||
		r.EvidenceClass != SubjectiveEvidence || r.Source != "user_feedback" {
		return ErrContract
	}
	return encodedWithin(r, 4096)
}

// ApprovalSummary is the complete browser allowlist. Prompt is a trusted,
// bounded, redacted explanation; raw arguments and binding digests have no field.
type ApprovalSummary struct {
	ID           string    `json:"id"`
	State        string    `json:"state"`
	Revision     int64     `json:"revision"`
	Prompt       string    `json:"prompt"`
	ScopeSummary string    `json:"scope_summary"`
	ToolName     string    `json:"tool_name"`
	ToolBehavior string    `json:"tool_behavior"`
	ExpiresAt    time.Time `json:"expires_at"`
	CanAllow     bool      `json:"can_allow"`
	CanDeny      bool      `json:"can_deny"`
	CanRevoke    bool      `json:"can_revoke"`
}

func (s ApprovalSummary) Validate() error {
	if !validID(s.ID) || s.Revision < 1 || requireText(s.Prompt, MaxApprovalPromptBytes) != nil ||
		requireText(s.ScopeSummary, MaxApprovalScopeBytes) != nil ||
		!validToolName(s.ToolName) || !validBrowserTime(s.ExpiresAt) ||
		(s.ToolBehavior != "read_only" && s.ToolBehavior != "idempotent_write" && s.ToolBehavior != "non_idempotent_write") {
		return ErrContract
	}
	switch s.State {
	case "pending":
		if s.Revision != 1 || !s.CanAllow || !s.CanDeny || s.CanRevoke {
			return ErrContract
		}
	case "approved":
		if s.Revision != 2 || s.CanAllow || s.CanDeny || !s.CanRevoke {
			return ErrContract
		}
	case "denied":
		if s.Revision != 2 || s.CanAllow || s.CanDeny || s.CanRevoke {
			return ErrContract
		}
	case "revoked", "consumed":
		if s.Revision != 3 || s.CanAllow || s.CanDeny || s.CanRevoke {
			return ErrContract
		}
	case "expired":
		if (s.Revision != 1 && s.Revision != 2) || s.CanAllow || s.CanDeny || s.CanRevoke {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return encodedWithin(s, MaxApprovalPromptBytes+MaxApprovalScopeBytes+2048)
}

type ApprovalPage struct {
	Version    int               `json:"version"`
	TaskID     string            `json:"task_id"`
	Items      []ApprovalSummary `json:"items"`
	NextCursor string            `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

func (p ApprovalPage) Validate() error {
	if p.Version != ContractVersion || !validID(p.TaskID) || p.Items == nil || len(p.Items) > MaxApprovalItems ||
		p.HasMore != (p.NextCursor != "") || (p.NextCursor != "" && !boundedPrintable(p.NextCursor, 1, MaxCursorBytes)) {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, item := range p.Items {
		if item.Validate() != nil || seen[item.ID] {
			return ErrContract
		}
		seen[item.ID] = true
	}
	return encodedWithin(p, MaxEventBytes)
}

type ApprovalDecisionReceipt struct {
	Version     int       `json:"version"`
	OperationID string    `json:"operation_id"`
	TaskID      string    `json:"task_id"`
	ApprovalID  string    `json:"approval_id"`
	State       string    `json:"state"`
	Revision    int64     `json:"revision"`
	DecidedAt   time.Time `json:"decided_at"`
}

func (r ApprovalDecisionReceipt) Validate() error {
	if r.Version != ContractVersion || !validID(r.OperationID) || !validID(r.TaskID) || !validID(r.ApprovalID) ||
		r.Revision < 2 || !validBrowserTime(r.DecidedAt) {
		return ErrContract
	}
	switch r.State {
	case "approved", "denied":
		if r.Revision != 2 {
			return ErrContract
		}
	case "revoked", "consumed":
		if r.Revision != 3 {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return encodedWithin(r, 4096)
}

func validToolName(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index := range len(value) {
		c := value[index]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || index > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// OperationSummary is a content-free, session-scoped reconciliation record.
// Request bodies, digests, responses, credentials and provider output are not
// representable. Subject is optional while a newly reserved operation or a
// pre-entity rejection has not produced a durable domain identifier. Rejections
// retain a safe subject whenever the rejected request identified one.
type OperationSummary struct {
	Version     int       `json:"version"`
	OperationID string    `json:"operation_id"`
	Action      string    `json:"action"`
	State       string    `json:"state"`
	SubjectType string    `json:"subject_type,omitempty"`
	SubjectID   string    `json:"subject_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s OperationSummary) Validate() error {
	if s.Version != ContractVersion || !validID(s.OperationID) || !validOperationAction(s.Action) ||
		(s.State != "pending" && s.State != "committed" && s.State != "rejected") || (s.SubjectType == "") != (s.SubjectID == "") ||
		!optionalID(s.SubjectID) || !validBrowserTime(s.CreatedAt) || !validBrowserTime(s.UpdatedAt) || s.UpdatedAt.Before(s.CreatedAt) {
		return ErrContract
	}
	if s.SubjectType != "" && s.SubjectType != "chat" && s.SubjectType != "task" && s.SubjectType != "submission" && s.SubjectType != "feedback" && s.SubjectType != "approval" {
		return ErrContract
	}
	if s.State == "committed" && s.SubjectID == "" {
		return ErrContract
	}
	return encodedWithin(s, 4096)
}

type OperationPage struct {
	Version    int                `json:"version"`
	Items      []OperationSummary `json:"items"`
	NextCursor string             `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
}

func (p OperationPage) Validate() error {
	if p.Version != ContractVersion || p.Items == nil || len(p.Items) > MaxOperationItems || (p.HasMore && len(p.Items) == 0) ||
		p.HasMore != (p.NextCursor != "") || (p.NextCursor != "" && !boundedPrintable(p.NextCursor, 1, MaxCursorBytes)) {
		return ErrContract
	}
	seen := make(map[string]bool, len(p.Items))
	for _, item := range p.Items {
		if item.Validate() != nil || seen[item.OperationID] {
			return ErrContract
		}
		seen[item.OperationID] = true
	}
	return encodedWithin(p, MaxEventBytes)
}

// SubmissionStatus is the browser-safe asynchronous submission projection.
// It deliberately excludes stored input, output, errors, leases, config
// digests, usage and route details.
type SubmissionStatus struct {
	Version      int       `json:"version"`
	SubmissionID string    `json:"submission_id"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	ChatID       string    `json:"chat_id,omitempty"`
	TaskID       string    `json:"task_id,omitempty"`
	Revision     *int64    `json:"revision,omitempty"`
	CanCancel    bool      `json:"can_cancel"`
}

func (s SubmissionStatus) Validate() error {
	if s.Version != ContractVersion || !validID(s.SubmissionID) || !validBrowserTime(s.CreatedAt) ||
		!validBrowserTime(s.UpdatedAt) || s.UpdatedAt.Before(s.CreatedAt) || !optionalID(s.ChatID) ||
		!optionalID(s.TaskID) || !optionalRevision(s.Revision) || (s.ChatID == "") != (s.TaskID == "") ||
		(s.Revision != nil) != (s.TaskID != "") {
		return ErrContract
	}
	switch s.State {
	case "queued", "running":
	case "completed":
		if s.TaskID == "" || s.CanCancel {
			return ErrContract
		}
	case "failed", "canceled":
		if s.CanCancel {
			return ErrContract
		}
	default:
		return ErrContract
	}
	if s.Revision != nil && *s.Revision < 1 {
		return ErrContract
	}
	return encodedWithin(s, 4096)
}

func validOperationAction(value string) bool {
	switch value {
	case "submit", "resume", "steer", "cancel", "cancel_submission", "record", "revise", "approval.allow", "approval.deny", "approval.revoke":
		return true
	default:
		return false
	}
}
