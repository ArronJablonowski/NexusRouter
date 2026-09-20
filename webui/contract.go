// Package webui defines the versioned browser-facing contract. It does not
// serve assets, authenticate browsers, persist boards, or execute tasks.
package webui

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ContractVersion       = 1
	MaxRequestBytes       = 1 << 20
	MaxEventBytes         = 1 << 20
	MaxCursorBytes        = 512
	MaxIDBytes            = 128
	MaxTitleBytes         = 256
	MaxDescriptionBytes   = 64 << 10
	MaxEvidenceBytes      = 64 << 10
	MaxIdempotencyBytes   = 128
	MinIdempotencyBytes   = 16
	MaxLabels             = 32
	MaxLabelBytes         = 64
	MaxDependencies       = 64
	MaxAcceptanceCriteria = 32
)

var (
	ErrContract      = errors.New("invalid web UI contract")
	idPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	modelIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	validatorPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9_-]*[A-Za-z0-9])?)*$`)
	operationPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
)

// ValidModelID exposes the browser contract's bounded model-alias grammar.
func ValidModelID(value string) bool { return modelIDPattern.MatchString(value) }

type ChatAction string

const (
	ChatSubmit           ChatAction = "submit"
	ChatResume           ChatAction = "resume"
	ChatSteer            ChatAction = "steer"
	ChatCancel           ChatAction = "cancel"
	ChatCancelSubmission ChatAction = "cancel_submission"
)

type ChatRequest struct {
	Version          int        `json:"version"`
	Action           ChatAction `json:"action"`
	IdempotencyKey   string     `json:"idempotency_key"`
	ChatID           string     `json:"chat_id,omitempty"`
	TaskID           string     `json:"task_id,omitempty"`
	SubmissionID     string     `json:"submission_id,omitempty"`
	ModelID          string     `json:"model_id,omitempty"`
	Text             string     `json:"text,omitempty"`
	ExpectedRevision *int64     `json:"expected_revision,omitempty"`
}

func (r ChatRequest) Validate() error {
	if r.Version != ContractVersion || !validKey(r.IdempotencyKey) ||
		!optionalID(r.ChatID) || !optionalID(r.TaskID) || !optionalID(r.SubmissionID) || !optionalModelID(r.ModelID) ||
		!optionalRevision(r.ExpectedRevision) || !boundedText(r.Text, MaxRequestBytes, true) {
		return ErrContract
	}
	switch r.Action {
	case ChatSubmit:
		if r.ChatID != "" || r.TaskID != "" || r.SubmissionID != "" || r.ExpectedRevision != nil || strings.TrimSpace(r.Text) == "" {
			return ErrContract
		}
	case ChatResume:
		if r.ChatID == "" || r.TaskID == "" || r.SubmissionID != "" || strings.TrimSpace(r.Text) == "" || r.ExpectedRevision == nil || *r.ExpectedRevision < 1 {
			return ErrContract
		}
	case ChatSteer:
		if r.TaskID == "" || r.SubmissionID != "" || strings.TrimSpace(r.Text) == "" || r.ModelID != "" || r.ChatID != "" || r.ExpectedRevision == nil || *r.ExpectedRevision < 1 {
			return ErrContract
		}
	case ChatCancel:
		if r.TaskID == "" || r.SubmissionID != "" || r.Text != "" || r.ModelID != "" || r.ChatID != "" || r.ExpectedRevision == nil || *r.ExpectedRevision < 1 {
			return ErrContract
		}
	case ChatCancelSubmission:
		if r.SubmissionID == "" || r.TaskID != "" || r.Text != "" || r.ModelID != "" || r.ChatID != "" || r.ExpectedRevision != nil {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return encodedWithin(r, MaxRequestBytes)
}

type ApprovalAction string

const (
	ApprovalAllow  ApprovalAction = "allow"
	ApprovalDeny   ApprovalAction = "deny"
	ApprovalRevoke ApprovalAction = "revoke"
)

type ApprovalRequest struct {
	Version          int            `json:"version"`
	IdempotencyKey   string         `json:"idempotency_key"`
	TaskID           string         `json:"task_id"`
	ApprovalID       string         `json:"approval_id"`
	Action           ApprovalAction `json:"action"`
	ExpectedRevision int64          `json:"expected_revision"`
}

func (r ApprovalRequest) Validate() error {
	if r.Version != ContractVersion || !validKey(r.IdempotencyKey) || !validID(r.TaskID) ||
		!validID(r.ApprovalID) || r.ExpectedRevision < 1 ||
		(r.Action != ApprovalAllow && r.Action != ApprovalDeny && r.Action != ApprovalRevoke) {
		return ErrContract
	}
	if (r.Action == ApprovalAllow || r.Action == ApprovalDeny) && r.ExpectedRevision != 1 ||
		r.Action == ApprovalRevoke && r.ExpectedRevision != 2 {
		return ErrContract
	}
	return encodedWithin(r, 16<<10)
}

type FeedbackAction string

const (
	FeedbackRecord FeedbackAction = "record"
	FeedbackRevise FeedbackAction = "revise"
)

type FeedbackRequest struct {
	Version          int            `json:"version"`
	IdempotencyKey   string         `json:"idempotency_key"`
	TaskID           string         `json:"task_id"`
	FeedbackID       string         `json:"feedback_id,omitempty"`
	Action           FeedbackAction `json:"action"`
	Accepted         bool           `json:"accepted"`
	AttemptCost      *float64       `json:"attempt_cost,omitempty"`
	ExpectedRevision *int64         `json:"expected_revision,omitempty"`
}

func (r FeedbackRequest) Validate() error {
	if r.Version != ContractVersion || !validKey(r.IdempotencyKey) || !validID(r.TaskID) ||
		!optionalID(r.FeedbackID) || (r.AttemptCost != nil && (math.IsNaN(*r.AttemptCost) || math.IsInf(*r.AttemptCost, 0) || *r.AttemptCost < 0)) ||
		!optionalRevision(r.ExpectedRevision) {
		return ErrContract
	}
	switch r.Action {
	case FeedbackRecord:
		if r.FeedbackID != "" || r.ExpectedRevision != nil || r.AttemptCost == nil {
			return ErrContract
		}
	case FeedbackRevise:
		if r.FeedbackID == "" || r.ExpectedRevision == nil || *r.ExpectedRevision < 1 || r.AttemptCost != nil {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return encodedWithin(r, 4<<10)
}

type BoardAction string

const (
	BoardCreate        BoardAction = "board.create"
	BoardRevise        BoardAction = "board.revise"
	BoardArchive       BoardAction = "board.archive"
	CardCreate         BoardAction = "card.create"
	CardRevise         BoardAction = "card.revise"
	CardMove           BoardAction = "card.move"
	CardReorder        BoardAction = "card.reorder"
	DependencyAdd      BoardAction = "dependency.add"
	DependencyRemove   BoardAction = "dependency.remove"
	CardClaim          BoardAction = "card.claim"
	ClaimHeartbeat     BoardAction = "claim.heartbeat"
	ClaimAttention     BoardAction = "claim.attention"
	CheckpointAppend   BoardAction = "checkpoint.append"
	CandidateSubmit    BoardAction = "candidate.submit"
	AcceptanceAccept   BoardAction = "acceptance.accept"
	AcceptanceReject   BoardAction = "acceptance.reject"
	CardPauseRequest   BoardAction = "card.pause_request"
	CardPauseAck       BoardAction = "card.pause_acknowledge"
	CardResumeRequest  BoardAction = "card.resume_request"
	CardResumeAck      BoardAction = "card.resume_acknowledge"
	CardCancelRequest  BoardAction = "card.cancel_request"
	CardCancelFinalize BoardAction = "card.cancel_finalize"
	CardBlock          BoardAction = "card.block"
	CardUnblock        BoardAction = "card.unblock"
	CriteriaRevise     BoardAction = "criteria.revise"
	ClaimRecover       BoardAction = "claim.recover"
	ClaimFail          BoardAction = "claim.fail"
)

type BoardRequest struct {
	Version                  int                   `json:"version"`
	Action                   BoardAction           `json:"action"`
	IdempotencyKey           string                `json:"idempotency_key"`
	BoardID                  string                `json:"board_id,omitempty"`
	CardID                   string                `json:"card_id,omitempty"`
	DependencyID             string                `json:"dependency_id,omitempty"`
	ClaimID                  string                `json:"claim_id,omitempty"`
	AttemptID                string                `json:"attempt_id,omitempty"`
	CandidateID              string                `json:"candidate_id,omitempty"`
	CriteriaRevision         *int64                `json:"criteria_revision,omitempty"`
	ExpectedCriteriaRevision *int64                `json:"expected_criteria_revision,omitempty"`
	EvidenceHeadRevision     *int64                `json:"evidence_head_revision,omitempty"`
	ExpectedLayoutRevision   *int64                `json:"expected_layout_revision,omitempty"`
	ExpectedGraphRevision    *int64                `json:"expected_graph_revision,omitempty"`
	ExpectedBoardRevision    *int64                `json:"expected_board_revision,omitempty"`
	ExpectedCardRevision     *int64                `json:"expected_card_revision,omitempty"`
	ExpectedClaimRevision    *int64                `json:"expected_claim_revision,omitempty"`
	Title                    *string               `json:"title,omitempty"`
	Description              *string               `json:"description,omitempty"`
	TargetState              string                `json:"target_state,omitempty"`
	ReasonCode               string                `json:"reason_code,omitempty"`
	Evidence                 string                `json:"evidence,omitempty"`
	CandidateDigest          string                `json:"candidate_digest,omitempty"`
	CriteriaDigest           string                `json:"criteria_digest,omitempty"`
	EvidenceSetDigest        string                `json:"evidence_set_digest,omitempty"`
	PolicyDigest             string                `json:"policy_digest,omitempty"`
	StopProofID              string                `json:"stop_proof_id,omitempty"`
	EffectResolution         string                `json:"effect_resolution,omitempty"`
	TaskHeadDigest           string                `json:"task_head_digest,omitempty"`
	ProcessProofDigest       string                `json:"process_proof_digest,omitempty"`
	EffectEvidenceDigest     string                `json:"effect_evidence_digest,omitempty"`
	Priority                 string                `json:"priority,omitempty"`
	ParentID                 string                `json:"parent_id,omitempty"`
	AssigneeID               string                `json:"assignee_id,omitempty"`
	ClearParent              *bool                 `json:"clear_parent,omitempty"`
	ClearAssignee            *bool                 `json:"clear_assignee,omitempty"`
	BeforeCardID             string                `json:"before_card_id,omitempty"`
	AfterCardID              string                `json:"after_card_id,omitempty"`
	Budget                   *WorkBudget           `json:"budget,omitempty"`
	Labels                   []string              `json:"labels,omitempty"`
	Dependencies             []string              `json:"dependencies,omitempty"`
	Criteria                 []AcceptanceCriterion `json:"criteria,omitempty"`
}

func (r BoardRequest) Validate() error {
	if r.Version != ContractVersion || !validKey(r.IdempotencyKey) || !optionalID(r.BoardID) ||
		!optionalID(r.CardID) || !optionalID(r.DependencyID) || !optionalID(r.ClaimID) ||
		!optionalID(r.AttemptID) || !optionalID(r.CandidateID) || !optionalRevision(r.CriteriaRevision) ||
		!optionalRevision(r.ExpectedCriteriaRevision) || !optionalRevision(r.EvidenceHeadRevision) ||
		!optionalRevision(r.ExpectedLayoutRevision) || !optionalRevision(r.ExpectedGraphRevision) ||
		!optionalRevision(r.ExpectedBoardRevision) || !optionalRevision(r.ExpectedCardRevision) ||
		!optionalRevision(r.ExpectedClaimRevision) || !optionalBoundedText(r.Title, MaxTitleBytes) ||
		!optionalBoundedText(r.Description, MaxDescriptionBytes) || !boundedText(r.Evidence, MaxEvidenceBytes, true) ||
		!optionalID(r.TargetState) || !optionalID(r.ReasonCode) || !optionalID(r.StopProofID) ||
		!optionalID(r.ParentID) || !optionalID(r.AssigneeID) || !optionalID(r.BeforeCardID) || !optionalID(r.AfterCardID) ||
		!validOptionalDigest(r.CandidateDigest) || !validOptionalDigest(r.CriteriaDigest) ||
		!validOptionalDigest(r.EvidenceSetDigest) || !validOptionalDigest(r.PolicyDigest) ||
		!validOptionalDigest(r.TaskHeadDigest) || !validOptionalDigest(r.ProcessProofDigest) || !validOptionalDigest(r.EffectEvidenceDigest) ||
		(r.Priority != "" && !validPriority(r.Priority)) || (r.Budget != nil && r.Budget.Validate() != nil) ||
		!validLabels(r.Labels) || !validIDs(r.Dependencies, MaxDependencies) {
		return ErrContract
	}
	if r.Action == BoardCreate {
		if r.BoardID != "" || r.Title == nil || strings.TrimSpace(*r.Title) == "" || r.CardID != "" {
			return ErrContract
		}
	} else if r.BoardID == "" {
		return ErrContract
	}
	switch r.Action {
	case BoardCreate:
	case BoardRevise:
		if revisionBelowOne(r.ExpectedBoardRevision) || r.Title == nil && r.Description == nil {
			return ErrContract
		}
	case BoardArchive:
		if revisionBelowOne(r.ExpectedBoardRevision) {
			return ErrContract
		}
	case CardCreate:
		if r.Title == nil || strings.TrimSpace(*r.Title) == "" || r.CardID != "" || revisionBelowOne(r.ExpectedBoardRevision) || revisionBelowOne(r.ExpectedGraphRevision) || validateCriteria(r.Criteria, 1) != nil {
			return ErrContract
		}
	case CardRevise, CardMove, CardReorder, DependencyAdd, DependencyRemove, CardPauseRequest, CardResumeRequest, CardCancelRequest:
		if r.CardID == "" || revisionBelowOne(r.ExpectedCardRevision) {
			return ErrContract
		}
		if r.Action == CardRevise && (!optionalTrue(r.ClearParent) || !optionalTrue(r.ClearAssignee)) {
			return ErrContract
		}
		if r.Action == CardRevise && r.Title == nil && r.Description == nil && r.Labels == nil &&
			r.Priority == "" && r.ParentID == "" && r.AssigneeID == "" && r.ClearParent == nil && r.ClearAssignee == nil && r.Budget == nil {
			return ErrContract
		}
		if r.Action == CardRevise && (r.ParentID != "" || r.ClearParent != nil) != (r.ExpectedGraphRevision != nil) ||
			r.Action == CardRevise && (r.ParentID != "" && r.ClearParent != nil || r.AssigneeID != "" && r.ClearAssignee != nil) {
			return ErrContract
		}
		if r.Action == CardMove && (!validMoveTarget(r.TargetState) || revisionBelowOne(r.ExpectedBoardRevision) ||
			revisionBelowOne(r.ExpectedLayoutRevision) || (r.BeforeCardID != "" && r.AfterCardID != "") ||
			r.BeforeCardID == r.CardID || r.AfterCardID == r.CardID) {
			return ErrContract
		}
		if r.Action == CardReorder && (revisionBelowOne(r.ExpectedBoardRevision) || revisionBelowOne(r.ExpectedLayoutRevision) ||
			(r.BeforeCardID == "") == (r.AfterCardID == "") || r.BeforeCardID == r.CardID || r.AfterCardID == r.CardID) {
			return ErrContract
		}
	case CriteriaRevise:
		if r.CardID == "" || revisionBelowOne(r.ExpectedCardRevision) || revisionBelowOne(r.ExpectedCriteriaRevision) || validateCriteria(r.Criteria, 1) != nil {
			return ErrContract
		}
	case AcceptanceAccept, AcceptanceReject:
		if r.CardID == "" || r.AttemptID == "" || r.CandidateID == "" || revisionBelowOne(r.CriteriaRevision) ||
			revisionBelowOne(r.ExpectedCardRevision) || r.EvidenceHeadRevision == nil || *r.EvidenceHeadRevision < 0 || strings.TrimSpace(r.Evidence) == "" ||
			!validWorkboardDigest(r.CandidateDigest) || !validWorkboardDigest(r.CriteriaDigest) ||
			!validWorkboardDigest(r.EvidenceSetDigest) || !validWorkboardDigest(r.PolicyDigest) {
			return ErrContract
		}
	default:
		return ErrContract
	}
	if (r.Action == DependencyAdd || r.Action == DependencyRemove) && r.DependencyID == "" {
		return ErrContract
	}
	if (r.Action == DependencyAdd || r.Action == DependencyRemove) && revisionBelowOne(r.ExpectedGraphRevision) {
		return ErrContract
	}
	if !r.hasOnlyBoardFields(allowedBoardFields(r.Action)...) {
		return ErrContract
	}
	return encodedWithin(r, MaxRequestBytes)
}

func validMoveTarget(value string) bool {
	switch value {
	case "backlog", "ready":
		return true
	default:
		return false
	}
}

func revisionBelowOne(value *int64) bool { return value == nil || *value < 1 }

func optionalTrue(value *bool) bool { return value == nil || *value }

func validOptionalDigest(value string) bool { return value == "" || validWorkboardDigest(value) }

type Durability string

const (
	Provisional Durability = "provisional"
	Committed   Durability = "committed"
)

type EventKind string

const (
	ChatSnapshot    EventKind = "chat.snapshot"
	ChatDelta       EventKind = "chat.delta"
	ChatFinal       EventKind = "chat.final"
	LifecycleEvent  EventKind = "lifecycle.event"
	ModelChanged    EventKind = "model.changed"
	ToolChanged     EventKind = "tool.changed"
	RouteChanged    EventKind = "route.changed"
	WorkerChanged   EventKind = "worker.changed"
	ErrorChanged    EventKind = "error.changed"
	TaskTerminal    EventKind = "task.terminal"
	ApprovalChanged EventKind = "approval.changed"
	FeedbackChanged EventKind = "feedback.changed"
	BoardChanged    EventKind = "board.changed"
	CheckpointEvent EventKind = "checkpoint"
	AttentionEvent  EventKind = "attention"
	StreamError     EventKind = "stream.error"
)

type Event struct {
	Version    int             `json:"version"`
	Cursor     string          `json:"cursor,omitempty"`
	Kind       EventKind       `json:"kind"`
	Durability Durability      `json:"durability"`
	Subject    string          `json:"subject"`
	Revision   int64           `json:"revision"`
	Data       json.RawMessage `json:"data"`
}

func (e Event) Validate() error {
	if e.Version != ContractVersion || !validID(e.Subject) || e.Revision < 0 ||
		!validJSONObject(e.Data) || len(e.Data) > MaxEventBytes {
		return ErrContract
	}
	switch e.Kind {
	case ChatDelta:
		if e.Durability != Provisional || e.Cursor != "" || e.Revision != 0 || validateEventData[ChatDeltaData](e.Data) != nil {
			return ErrContract
		}
	case ChatSnapshot:
		data, err := parseEventData[ChatSnapshotData](e.Data)
		if !e.validCommitted() || err != nil || !data.fitsRevision(e.Revision) {
			return ErrContract
		}
	case ChatFinal:
		data, err := parseEventData[ChatFinalData](e.Data)
		if !e.validCommitted() || err != nil || data.Message.Revision != e.Revision {
			return ErrContract
		}
	case LifecycleEvent:
		if !e.validCommitted() || validateEventData[LifecycleData](e.Data) != nil {
			return ErrContract
		}
	case ModelChanged:
		if !e.validCommitted() || validateEventData[ModelChangedData](e.Data) != nil {
			return ErrContract
		}
	case ToolChanged:
		if !e.validCommitted() || validateEventData[ToolChangedData](e.Data) != nil {
			return ErrContract
		}
	case RouteChanged:
		if !e.validCommitted() || validateEventData[RouteChangedData](e.Data) != nil {
			return ErrContract
		}
	case WorkerChanged:
		if !e.validCommitted() || validateEventData[WorkerChangedData](e.Data) != nil {
			return ErrContract
		}
	case ErrorChanged:
		if !e.validCommitted() || validateEventData[ErrorChangedData](e.Data) != nil {
			return ErrContract
		}
	case TaskTerminal:
		if !e.validCommitted() || validateEventData[TaskTerminalData](e.Data) != nil {
			return ErrContract
		}
	case ApprovalChanged:
		if !e.validCommitted() || validateEventData[ApprovalChangedData](e.Data) != nil {
			return ErrContract
		}
	case FeedbackChanged:
		if !e.validCommitted() || validateEventData[FeedbackChangedData](e.Data) != nil {
			return ErrContract
		}
	case BoardChanged:
		if !e.validCommitted() || validateEventData[BoardChangedData](e.Data) != nil {
			return ErrContract
		}
	case CheckpointEvent:
		if !e.validCommitted() || validateEventData[CheckpointData](e.Data) != nil {
			return ErrContract
		}
	case AttentionEvent:
		if !e.validCommitted() || validateEventData[AttentionData](e.Data) != nil {
			return ErrContract
		}
	case StreamError:
		if (e.Durability == Provisional && (e.Cursor != "" || e.Revision != 0)) ||
			(e.Durability == Committed && !e.validCommitted()) ||
			(e.Durability != Provisional && e.Durability != Committed) || validateEventData[StreamErrorData](e.Data) != nil {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return encodedWithin(e, MaxEventBytes)
}

func (e Event) validCommitted() bool {
	return e.Durability == Committed && e.Revision > 0 && boundedPrintable(e.Cursor, 1, MaxCursorBytes)
}

type ChatDeltaData struct {
	TaskID string `json:"task_id"`
	Text   string `json:"text"`
}

func (d ChatDeltaData) Validate() error {
	if !validID(d.TaskID) {
		return ErrContract
	}
	return requireText(d.Text, MaxEventBytes)
}

type PresentationMessage struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Text     string `json:"text"`
	Revision int64  `json:"revision"`
}

func (m PresentationMessage) Validate() error {
	if !validID(m.ID) || (m.Role != "user" && m.Role != "assistant") || m.Revision < 1 {
		return ErrContract
	}
	return requireText(m.Text, MaxEventBytes)
}

type ChatSnapshotData struct {
	ChatID     string                `json:"chat_id"`
	Messages   []PresentationMessage `json:"messages"`
	NextCursor string                `json:"next_cursor,omitempty"`
	HasMore    bool                  `json:"has_more"`
}

func (d ChatSnapshotData) Validate() error {
	if !validID(d.ChatID) || len(d.Messages) > 100 || d.HasMore != (d.NextCursor != "") ||
		(d.NextCursor != "" && !boundedPrintable(d.NextCursor, 1, MaxCursorBytes)) {
		return ErrContract
	}
	for _, message := range d.Messages {
		if message.Validate() != nil {
			return ErrContract
		}
	}
	return nil
}

func (d ChatSnapshotData) fitsRevision(revision int64) bool {
	previous := int64(0)
	for _, message := range d.Messages {
		if message.Revision <= previous || message.Revision > revision {
			return false
		}
		previous = message.Revision
	}
	return true
}

type ChatFinalData struct {
	TaskID  string              `json:"task_id"`
	State   string              `json:"state"`
	Message PresentationMessage `json:"message"`
}

func (d ChatFinalData) Validate() error {
	if !validID(d.TaskID) || d.State != "completed" {
		return ErrContract
	}
	return d.Message.Validate()
}

type LifecycleData struct {
	TaskID string `json:"task_id"`
	State  string `json:"state"`
	Code   string `json:"code,omitempty"`
}

func (d LifecycleData) Validate() error {
	if !validID(d.TaskID) || !validID(d.State) || !optionalID(d.Code) {
		return ErrContract
	}
	return nil
}

type ApprovalChangedData struct {
	TaskID     string `json:"task_id"`
	ApprovalID string `json:"approval_id"`
	State      string `json:"state"`
}

func (d ApprovalChangedData) Validate() error {
	if !validID(d.TaskID) || !validID(d.ApprovalID) || !validID(d.State) {
		return ErrContract
	}
	return nil
}

type FeedbackChangedData struct {
	TaskID     string `json:"task_id"`
	FeedbackID string `json:"feedback_id"`
	State      string `json:"state"`
}

func (d FeedbackChangedData) Validate() error {
	if !validID(d.TaskID) || !validID(d.FeedbackID) || !validID(d.State) {
		return ErrContract
	}
	return nil
}

type BoardChangedData struct {
	BoardID string `json:"board_id"`
	CardID  string `json:"card_id,omitempty"`
	Change  string `json:"change"`
	State   string `json:"state,omitempty"`
}

func (d BoardChangedData) Validate() error {
	if !validID(d.BoardID) || !optionalID(d.CardID) {
		return ErrContract
	}
	switch d.Change {
	case "board_created", "board_revised":
		if d.CardID != "" || d.State != "" {
			return ErrContract
		}
	case "card_changed":
		// State is an optional optimization. Durable workboard journal events
		// deliberately omit command bodies, so clients must be able to treat a
		// card change as an invalidation and reconcile the board snapshot.
		if d.CardID == "" || d.State != "" && !validBoardState(d.State) {
			return ErrContract
		}
	case "dependency_changed", "claim_changed", "evidence_changed", "acceptance_changed":
		if d.CardID == "" || d.State != "" {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return nil
}

type CheckpointData struct {
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

func (d CheckpointData) Validate() error {
	if !boundedPrintable(d.NextCursor, 1, MaxCursorBytes) {
		return ErrContract
	}
	return nil
}

type AttentionData struct {
	Code    string `json:"code"`
	Subject string `json:"subject"`
}

func (d AttentionData) Validate() error {
	if !validID(d.Code) || !validID(d.Subject) {
		return ErrContract
	}
	return nil
}

type StreamErrorData struct {
	Error Error `json:"error"`
}

func (d StreamErrorData) Validate() error { return d.Error.Validate() }

type eventData interface{ Validate() error }

func validateEventData[T eventData](body json.RawMessage) error {
	_, err := parseEventData[T](body)
	return err
}

func parseEventData[T eventData](body json.RawMessage) (T, error) {
	var data T
	if rejectDuplicateObjectKeys(body) != nil {
		return data, ErrContract
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil || decoder.Decode(new(any)) != io.EOF || data.Validate() != nil {
		return data, ErrContract
	}
	return data, nil
}

func validJSONObject(body json.RawMessage) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) >= 2 && trimmed[0] == '{' && json.Valid(trimmed)
}

func rejectDuplicateObjectKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if rejectDuplicateValue(decoder) != nil {
		return ErrContract
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrContract
	}
	return nil
}

// RejectDuplicateJSONFields validates one complete JSON value and rejects
// duplicate object members at every nesting level.
func RejectDuplicateJSONFields(body []byte) error {
	return rejectDuplicateObjectKeys(body)
}

func rejectDuplicateValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return ErrContract
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, isString := keyToken.(string)
			if err != nil || !isString {
				return ErrContract
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrContract
			}
			seen[key] = struct{}{}
			if rejectDuplicateValue(decoder) != nil {
				return ErrContract
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return ErrContract
		}
	case '[':
		for decoder.More() {
			if rejectDuplicateValue(decoder) != nil {
				return ErrContract
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return nil
}

func validBoardState(value string) bool {
	switch value {
	case "backlog", "ready", "in_progress", "blocked", "review", "done", "canceled":
		return true
	default:
		return false
	}
}

type Error struct {
	Version         int    `json:"version"`
	Code            string `json:"code"`
	Message         string `json:"message"`
	Retryable       bool   `json:"retryable"`
	CurrentRevision *int64 `json:"current_revision,omitempty"`
	RetryAfterMS    *int64 `json:"retry_after_ms,omitempty"`
	SubjectType     string `json:"subject_type,omitempty"`
	SubjectID       string `json:"subject_id,omitempty"`
	CurrentState    string `json:"current_state,omitempty"`
	OperationID     string `json:"operation_id,omitempty"`
}

func (e Error) Validate() error {
	if e.Version != ContractVersion || !validID(e.Code) || !boundedText(e.Message, 256, false) ||
		!optionalRevision(e.CurrentRevision) || (e.RetryAfterMS != nil && (*e.RetryAfterMS < 1 || *e.RetryAfterMS > 60_000)) ||
		!optionalID(e.SubjectID) || !optionalID(e.CurrentState) || !optionalID(e.OperationID) ||
		(e.SubjectType == "") != (e.SubjectID == "") {
		return ErrContract
	}
	if e.SubjectType != "" && e.SubjectType != "chat" && e.SubjectType != "task" && e.SubjectType != "submission" && e.SubjectType != "feedback" && e.SubjectType != "approval" && e.SubjectType != "board" && e.SubjectType != "card" && e.SubjectType != "claim" {
		return ErrContract
	}
	return encodedWithin(e, 4096)
}

type OperationSpec struct {
	Operation, Method, BrowserPath, ServicePrimitive string
	Security                                         SecurityClass
	PrimitiveExists                                  bool
	Mutation                                         bool
}

type SecurityClass string

const (
	BootstrapChallenge  SecurityClass = "bootstrap_challenge"
	ChallengeBound      SecurityClass = "challenge_bound"
	SessionCSRFRefresh  SecurityClass = "session_csrf_refresh"
	SessionRead         SecurityClass = "session_read"
	SessionCSRFMutation SecurityClass = "session_csrf_mutation"
)

// Operations returns an owned, closed mapping from proposed UI operations to
// current application primitives or clearly named future endpoints.
func Operations() []OperationSpec {
	return append([]OperationSpec(nil), operationSpecs...)
}

// OperationsAtBase rebases the canonical operation map for a configured shell
// mount without changing operation identities or application primitives.
func OperationsAtBase(basePath string) ([]OperationSpec, error) {
	basePath, ok := normalizeShellBasePath(basePath)
	if !ok {
		return nil, ErrContract
	}
	operations := Operations()
	for index := range operations {
		operations[index].BrowserPath = basePath + strings.TrimPrefix(operations[index].BrowserPath, DefaultShellBasePath)
	}
	return operations, nil
}

var operationSpecs = []OperationSpec{
	{Operation: "session.challenge", Method: "POST", BrowserPath: "/app/api/v1/session/challenges", ServicePrimitive: "new browser-session challenge", Security: BootstrapChallenge, Mutation: true},
	{Operation: "session.complete", Method: "POST", BrowserPath: "/app/api/v1/session", ServicePrimitive: "new approved challenge consumption", Security: ChallengeBound, Mutation: true},
	{Operation: "session.csrf", Method: "POST", BrowserPath: "/app/api/v1/session/csrf", ServicePrimitive: "new same-origin session CSRF rotation", Security: SessionCSRFRefresh, Mutation: true},
	{Operation: "session.logout", Method: "POST", BrowserPath: "/app/api/v1/session/logout", ServicePrimitive: "new browser-session revocation", Security: SessionCSRFMutation, Mutation: true},
	{Operation: "chat.list", Method: "GET", BrowserPath: "/app/api/v1/chats", ServicePrimitive: "new task/session presentation projection", Security: SessionRead},
	{Operation: "chat.history", Method: "GET", BrowserPath: "/app/api/v1/chats/{chat}/messages", ServicePrimitive: "new bounded presentation projection", Security: SessionRead},
	{Operation: "chat.submit", Method: "POST", BrowserPath: "/app/api/v1/chats", ServicePrimitive: "POST /v1/submissions application primitive", Security: SessionCSRFMutation, PrimitiveExists: true, Mutation: true},
	{Operation: "chat.resume", Method: "POST", BrowserPath: "/app/api/v1/chats/{chat}/resume", ServicePrimitive: "new revision-fenced facade over task continuation", Security: SessionCSRFMutation, Mutation: true},
	{Operation: "chat.stream", Method: "GET", BrowserPath: "/app/api/v1/chats/{chat}/events", ServicePrimitive: "presentation SSE over durable committed event reader", Security: SessionRead, PrimitiveExists: true},
	{Operation: "chat.steer", Method: "POST", BrowserPath: "/app/api/v1/tasks/{task}/steering", ServicePrimitive: "POST /v1/tasks/{task}/steering", Security: SessionCSRFMutation, PrimitiveExists: true, Mutation: true},
	{Operation: "chat.cancel", Method: "POST", BrowserPath: "/app/api/v1/tasks/{task}/cancel", ServicePrimitive: "POST /v1/tasks/{task}/cancel", Security: SessionCSRFMutation, PrimitiveExists: true, Mutation: true},
	{Operation: "chat.cancel_submission", Method: "POST", BrowserPath: "/app/api/v1/submissions/{submission}/cancel", ServicePrimitive: "POST /v1/submissions/{submission}/cancel", Security: SessionCSRFMutation, PrimitiveExists: true, Mutation: true},
	{Operation: "operation.list", Method: "GET", BrowserPath: "/app/api/v1/operations", ServicePrimitive: "new session-scoped browser operation projection", Security: SessionRead},
	{Operation: "submission.inspect", Method: "GET", BrowserPath: "/app/api/v1/submissions/{submission}", ServicePrimitive: "GET /v1/submissions/{submission}", Security: SessionRead, PrimitiveExists: true},
	{Operation: "task.controls", Method: "GET", BrowserPath: "/app/api/v1/tasks/{task}/controls", ServicePrimitive: "new browser-safe task control projection", Security: SessionRead},
	{Operation: "feedback.record", Method: "POST", BrowserPath: "/app/api/v1/feedback", ServicePrimitive: "new browser feedback facade over existing evidence service", Security: SessionCSRFMutation, Mutation: true},
	{Operation: "feedback.revise", Method: "POST", BrowserPath: "/app/api/v1/feedback/revisions", ServicePrimitive: "new revision facade over immutable feedback evidence", Security: SessionCSRFMutation, Mutation: true},
	{Operation: "feedback.inspect", Method: "GET", BrowserPath: "/app/api/v1/tasks/{task}/feedback", ServicePrimitive: "new browser-safe objective and subjective evidence projection", Security: SessionRead},
	{Operation: "approval.list", Method: "GET", BrowserPath: "/app/api/v1/tasks/{task}/approvals", ServicePrimitive: "GET /v1/tasks/{task}/approvals", Security: SessionRead, PrimitiveExists: true},
	{Operation: "approval.decide", Method: "POST", BrowserPath: "/app/api/v1/tasks/{task}/approvals/{approval}/decision", ServicePrimitive: "new revision facade over existing approval command", Security: SessionCSRFMutation, Mutation: true},
	{Operation: "model.list", Method: "GET", BrowserPath: "/app/api/v1/models", ServicePrimitive: "GET /v1/routing/models redacted catalog", Security: SessionRead, PrimitiveExists: true},
	{Operation: "route.inspect", Method: "GET", BrowserPath: "/app/api/v1/tasks/{task}/route", ServicePrimitive: "GET /v1/tasks/{task}/route", Security: SessionRead, PrimitiveExists: true},
	{Operation: "usage.inspect", Method: "GET", BrowserPath: "/app/api/v1/tasks/{task}/usage", ServicePrimitive: "bounded routed and auxiliary usage inspection projection", Security: SessionRead, PrimitiveExists: true},
	{Operation: "tool.inspect", Method: "GET", BrowserPath: "/app/api/v1/tasks/{task}/tools", ServicePrimitive: "bounded paired tool lifecycle projection", Security: SessionRead},
	{Operation: "audit.inspect", Method: "GET", BrowserPath: "/app/api/v1/tasks/{task}/audits", ServicePrimitive: "bounded orchestrator audit provenance projection", Security: SessionRead, PrimitiveExists: true},
	{Operation: "health.inspect", Method: "GET", BrowserPath: "/app/api/v1/health", ServicePrimitive: "GET /v1/health", Security: SessionRead, PrimitiveExists: true},
	{Operation: "resource.inspect", Method: "GET", BrowserPath: "/app/api/v1/resources", ServicePrimitive: "new bounded resource/pressure projection", Security: SessionRead},
	{Operation: "board.list", Method: "GET", BrowserPath: "/app/api/v1/workboards", ServicePrimitive: "new GET /v1/workboards", Security: SessionRead},
	{Operation: "board.create", Method: "POST", BrowserPath: "/app/api/v1/workboards", ServicePrimitive: "new POST /v1/workboards", Security: SessionCSRFMutation, Mutation: true},
	{Operation: "board.read", Method: "GET", BrowserPath: "/app/api/v1/workboards/{board}", ServicePrimitive: "new bounded workboard snapshot projection", Security: SessionRead},
	{Operation: "board.dependencies", Method: "GET", BrowserPath: "/app/api/v1/workboards/{board}/cards/{card}/dependencies", ServicePrimitive: "new bounded dependency projection", Security: SessionRead},
	{Operation: "board.attempts", Method: "GET", BrowserPath: "/app/api/v1/workboards/{board}/cards/{card}/attempts", ServicePrimitive: "new bounded attempt-history projection", Security: SessionRead},
	{Operation: "board.attempt", Method: "GET", BrowserPath: "/app/api/v1/workboards/{board}/cards/{card}/attempts/{attempt}", ServicePrimitive: "new bounded attempt-detail projection", Security: SessionRead},
	{Operation: "board.mutate", Method: "POST", BrowserPath: "/app/api/v1/workboards/{board}/operations", ServicePrimitive: "new POST /v1/workboards/{board}/operations", Security: SessionCSRFMutation, Mutation: true},
	{Operation: "board.stream", Method: "GET", BrowserPath: "/app/api/v1/workboards/{board}/events", ServicePrimitive: "new GET /v1/workboards/{board}/events", Security: SessionRead},
}

func validID(value string) bool { return idPattern.MatchString(value) }

func validValidatorID(value string) bool {
	return len(value) <= MaxIDBytes && validatorPattern.MatchString(value)
}

func optionalID(value string) bool { return value == "" || validID(value) }

func optionalModelID(value string) bool { return value == "" || modelIDPattern.MatchString(value) }

func optionalRevision(value *int64) bool { return value == nil || *value >= 0 }

func validKey(value string) bool {
	if len(value) < MinIdempotencyBytes || len(value) > MaxIdempotencyBytes {
		return false
	}
	for index := range len(value) {
		if value[index] < 33 || value[index] > 126 {
			return false
		}
	}
	return true
}

func requireText(value string, max int) error {
	if !boundedText(value, max, false) {
		return ErrContract
	}
	return nil
}

func boundedPrintable(value string, min, max int) bool {
	if len(value) < min || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func boundedText(value string, max int, allowEmpty bool) bool {
	if len(value) > max || !utf8.ValidString(value) || (!allowEmpty && strings.TrimSpace(value) == "") {
		return false
	}
	for _, r := range value {
		if r != '\n' && r != '\r' && r != '\t' && unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func optionalBoundedText(value *string, max int) bool {
	return value == nil || boundedText(*value, max, true)
}

func validLabels(values []string) bool {
	if len(values) > MaxLabels {
		return false
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !boundedText(value, MaxLabelBytes, false) {
			return false
		}
		key := strings.ToLower(strings.TrimSpace(value))
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func validIDs(values []string, max int) bool {
	if len(values) > max {
		return false
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !validID(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func encodedWithin(value any, max int) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) > max {
		return ErrContract
	}
	return nil
}
