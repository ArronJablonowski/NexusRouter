// Package runtime defines the provider-neutral protocol shared by all adapters.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

type Kind string

const (
	TaskStarted        Kind = "task.started"
	TaskCompleted      Kind = "task.completed"
	TaskFailed         Kind = "task.failed"
	TaskCanceled       Kind = "task.canceled"
	TurnStarted        Kind = "turn.started"
	TurnCompleted      Kind = "turn.completed"
	ModelDelta         Kind = "model.delta"
	ToolStarted        Kind = "tool.started"
	ToolCompleted      Kind = "tool.completed"
	WorkerStarted      Kind = "worker.started"
	WorkerHeartbeat    Kind = "worker.heartbeat"
	WorkerCompleted    Kind = "worker.completed"
	RouteSelected      Kind = "route.selected"
	EvaluationRecorded Kind = "evaluation.recorded"
	ErrorRecorded      Kind = "error.recorded"
	SteeringApplied    Kind = "steering.applied"
	ContextCompacted   Kind = "context.compacted"
)

type Effect string

const (
	NoEffect        Effect = "none"
	ConfirmedEffect Effect = "confirmed"
	UncertainEffect Effect = "uncertain"
)

// Event is a versioned immutable fact. Sequence is assigned per task by its
// coordinator, then committed by the store with an expected-sequence check.
// Content is session data, not safe for logging or telemetry export by default.
type Event struct {
	Version       int       `json:"version"`
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	SessionID     string    `json:"session_id"`
	Sequence      int64     `json:"sequence"`
	Time          time.Time `json:"time"`
	Kind          Kind      `json:"kind"`
	TurnID        string    `json:"turn_id,omitempty"`
	AttemptID     string    `json:"attempt_id,omitempty"`
	WorkerID      string    `json:"worker_id,omitempty"`
	RouteID       string    `json:"route_id,omitempty"`
	CausationID   string    `json:"causation_id,omitempty"`
	CorrelationID string    `json:"correlation_id"`
	Data          Data      `json:"data"`
}

type Data struct {
	SkillContext          *SkillContextUse               `json:"skill_context,omitempty"`
	IntentClassification  *IntentClassificationUse       `json:"intent_classification,omitempty"`
	DelegationCompaction  *DelegationCompactionAuthority `json:"delegation_compaction_authority,omitempty"`
	SteeringID            string                         `json:"steering_id,omitempty"`
	SubmissionID          string                         `json:"submission_id,omitempty"`
	Compaction            *ContextCompaction             `json:"compaction,omitempty"`
	ContextLineage        *ContextLineage                `json:"context_lineage,omitempty"`
	Validation            string                         `json:"validation,omitempty"`
	RetryOfTaskID         string                         `json:"retry_of_task_id,omitempty"`
	RouteCandidates       []routing.Candidate            `json:"route_candidates,omitempty"`
	RoutePolicy           *routing.Policy                `json:"route_policy,omitempty"`
	Resources             *resources.Snapshot            `json:"resources,omitempty"`
	Route                 *routing.Selection             `json:"route,omitempty"`
	RouteEstimatedCost    *float64                       `json:"route_estimated_cost,omitempty"`
	Domain                string                         `json:"domain,omitempty"`
	Profile               string                         `json:"profile,omitempty"`
	Capabilities          []string                       `json:"capabilities,omitempty"`
	ConfigID              string                         `json:"config_id,omitempty"`
	ParentTaskID          string                         `json:"parent_task_id,omitempty"`
	DelegationOrigin      *DelegationOrigin              `json:"delegation_origin,omitempty"`
	DelegationAuditIntent *DelegationAuditIntent         `json:"delegation_audit_intent,omitempty"`
	DelegationAudit       *DelegationAudit               `json:"delegation_audit,omitempty"`
	Privacy               string                         `json:"privacy,omitempty"`
	Text                  string                         `json:"text,omitempty"`
	ModelID               string                         `json:"model_id,omitempty"`
	ProviderID            string                         `json:"provider_id,omitempty"`
	ToolCallID            string                         `json:"tool_call_id,omitempty"`
	ToolName              string                         `json:"tool_name,omitempty"`
	ToolBehavior          ToolBehavior                   `json:"tool_behavior,omitempty"`
	Effect                Effect                         `json:"effect,omitempty"`
	Code                  string                         `json:"code,omitempty"`
	Accepted              *bool                          `json:"accepted,omitempty"`
	Messages              []providers.Message            `json:"messages,omitempty"`
	ReplacedMessages      int                            `json:"replaced_messages,omitempty"`
	ToolCalls             []providers.ToolCall           `json:"tool_calls,omitempty"`
	Usage                 *providers.Usage               `json:"usage,omitempty"`
	FinishReason          string                         `json:"finish_reason,omitempty"`
	ContextTokens         int                            `json:"context_tokens,omitempty"`
}

func (e Event) Validate() error {
	if e.Data.ContextTokens != 0 && (e.Kind != TaskStarted || e.Data.ContextTokens < 1) {
		return errors.New("invalid context tokens placement")
	}
	if e.Data.DelegationCompaction != nil {
		authority := e.Data.DelegationCompaction
		if authority.Validate() != nil || e.Kind != TaskStarted || e.WorkerID == "" ||
			e.Data.ParentTaskID != authority.RootTaskID || e.Data.DelegationOrigin == nil || e.Data.DelegationOrigin.Validate() != nil {
			return errors.New("invalid delegation compaction authority placement")
		}
	}
	if e.Data.Compaction != nil && e.Data.Compaction.Version == 2 && e.Data.ContextLineage == nil {
		return errors.New("version two compaction requires context lineage")
	}
	if e.Data.ContextLineage != nil {
		if (e.Kind != TaskStarted && e.Kind != ContextCompacted) || e.Data.ContextLineage.Validate() != nil {
			return errors.New("invalid context lineage")
		}
		last := e.Data.ContextLineage.Epochs[len(e.Data.ContextLineage.Epochs)-1]
		if e.Data.Compaction != nil {
			if last.TaskID != e.TaskID || last.ActivationSequence != e.Sequence || !reflect.DeepEqual(last.Compaction, *e.Data.Compaction) {
				return errors.New("invalid context lineage attribution")
			}
		} else if last.TaskID == e.TaskID {
			return errors.New("invalid inherited context lineage")
		}
	}
	if e.Data.IntentClassification != nil && (e.Kind != TaskStarted || e.Data.IntentClassification.Validate() != nil) {
		return errors.New("invalid intent classification attribution placement")
	}
	if len(e.Data.Capabilities) > 0 && (e.Kind != TaskStarted || !validTaskCapabilities(e.Data.Capabilities)) {
		return errors.New("invalid task capabilities placement")
	}
	if e.Data.ConfigID != "" && (!validConfigID(e.Data.ConfigID) || (e.Kind != TaskStarted && e.Kind != RouteSelected)) {
		return errors.New("invalid configuration identity")
	}
	if e.Data.RouteEstimatedCost != nil && (e.Kind != TaskStarted || math.IsNaN(*e.Data.RouteEstimatedCost) || math.IsInf(*e.Data.RouteEstimatedCost, 0) || *e.Data.RouteEstimatedCost < 0) {
		return errors.New("invalid route estimated cost")
	}
	if e.Data.SkillContext != nil && (e.Kind != TaskStarted || e.Data.SkillContext.Validate() != nil) {
		return errors.New("invalid skill context attribution placement")
	}
	if e.Data.ToolBehavior != "" && (!e.Data.ToolBehavior.Valid() || (e.Kind != ToolStarted && e.Kind != ToolCompleted)) {
		return errors.New("invalid tool behavior declaration")
	}
	if e.Data.DelegationOrigin != nil && (e.Kind != TaskStarted || e.Data.ParentTaskID == "" || e.Data.DelegationOrigin.Validate() != nil) {
		return errors.New("invalid delegation origin placement")
	}
	if e.Data.DelegationAuditIntent != nil && (e.Kind != TaskStarted || e.WorkerID == "" || e.Data.DelegationAuditIntent.Validate() != nil) {
		return errors.New("invalid delegation audit intent placement")
	}
	if e.Data.DelegationAudit != nil {
		// WorkerCompleted carries the outcome while TaskStarted carries the
		// intent, so cross-event binding is enforced by worker projectors.
		intent := &DelegationAuditIntent{Version: 1, OperationID: e.Data.DelegationAudit.OperationID, ReviewerID: e.Data.DelegationAudit.ReviewerID}
		if e.Kind != WorkerCompleted || e.Data.DelegationAudit.Validate(intent) != nil {
			return errors.New("invalid delegation audit placement")
		}
	}
	if e.Data.SteeringID != "" && e.Kind != SteeringApplied {
		return errors.New("invalid steering identity")
	}
	if id := e.Data.SubmissionID; id != "" {
		if e.Kind != TaskStarted || len(id) > 128 {
			return errors.New("invalid submission identity")
		}
		for _, c := range id {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				return errors.New("invalid submission identity")
			}
		}
	}
	if e.Version != 1 || e.ID == "" || e.TaskID == "" || e.SessionID == "" || e.CorrelationID == "" || e.Sequence < 1 || e.Time.IsZero() {
		return errors.New("invalid event envelope")
	}
	if e.Data.Compaction != nil && ((e.Kind != TaskStarted && e.Kind != ContextCompacted) || e.Data.Compaction.Validate(e.Data.ParentTaskID) != nil) {
		return errors.New("invalid context compaction")
	}
	if e.Data.ReplacedMessages != 0 && e.Kind != ContextCompacted {
		return errors.New("invalid replaced message count")
	}
	switch e.Kind {
	case ContextCompacted:
		if e.Data.Compaction == nil || e.Data.Compaction.SummaryAttemptID == "" || e.Data.Compaction.SummaryReviewID == "" || e.Data.ParentTaskID == "" || e.Data.ReplacedMessages < 1 || len(e.Data.Messages) == 0 || providers.ValidateMessages(e.Data.Messages) != nil || e.TurnID != "" || e.AttemptID != "" {
			return errors.New("invalid context compaction event")
		}
	case SteeringApplied:
		if !validSteeringID(e.Data.SteeringID) || !ValidSteeringText(e.Data.Text) || e.TurnID != "" || e.AttemptID != "" {
			return errors.New("invalid steering event")
		}
	case TaskStarted, TaskCompleted, TaskFailed, TaskCanceled:
	case TurnStarted, TurnCompleted, ModelDelta:
		if e.TurnID == "" {
			return errors.New("turn event requires turn ID")
		}
	case ToolStarted, ToolCompleted:
		if e.TurnID == "" || e.Data.ToolCallID == "" || e.Data.ToolName == "" {
			return errors.New("tool event requires turn and tool identity")
		}
		if e.Data.Effect != NoEffect && e.Data.Effect != ConfirmedEffect && e.Data.Effect != UncertainEffect {
			return errors.New("tool event requires effect evidence")
		}
	case WorkerStarted, WorkerHeartbeat, WorkerCompleted:
		if e.WorkerID == "" {
			return errors.New("worker event requires worker ID")
		}
	case RouteSelected:
		if e.RouteID == "" || e.Data.ModelID == "" || e.Data.ProviderID == "" {
			return errors.New("route event requires model and provider identity")
		}
	case EvaluationRecorded:
		if e.Data.Accepted == nil {
			return errors.New("evaluation requires acceptance outcome")
		}
	case ErrorRecorded:
		if e.Data.Code == "" {
			return errors.New("error event requires code")
		}
	default:
		return errors.New("unsupported event kind")
	}
	return nil
}

func validConfigID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (e Event) Encode() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	e.Time = e.Time.UTC()
	return json.Marshal(e)
}

// Clone returns an owned, canonical copy suitable for delivery across a
// process-local extension boundary. Event contains nested slices and pointers,
// so a shallow assignment is not an immutable projection.
func (e Event) Clone() (Event, error) {
	raw, err := e.Encode()
	if err != nil {
		return Event{}, err
	}
	var clone Event
	if err := json.Unmarshal(raw, &clone); err != nil {
		return Event{}, err
	}
	return clone, nil
}

// EventSink acknowledges an event only after its implementation accepts it.
type EventSink interface {
	Emit(context.Context, Event) error
}

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc func(context.Context, Event) error

func (f EventSinkFunc) Emit(ctx context.Context, event Event) error {
	if f == nil {
		return errors.New("nil event sink")
	}
	return f(ctx, event)
}
