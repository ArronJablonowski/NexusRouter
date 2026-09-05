// Package runtime defines the provider-neutral protocol shared by all adapters.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
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
	SteeringID      string               `json:"steering_id,omitempty"`
	SubmissionID    string               `json:"submission_id,omitempty"`
	Compaction      *ContextCompaction   `json:"compaction,omitempty"`
	Validation      string               `json:"validation,omitempty"`
	RetryOfTaskID   string               `json:"retry_of_task_id,omitempty"`
	RouteCandidates []routing.Candidate  `json:"route_candidates,omitempty"`
	RoutePolicy     *routing.Policy      `json:"route_policy,omitempty"`
	Resources       *resources.Snapshot  `json:"resources,omitempty"`
	Route           *routing.Selection   `json:"route,omitempty"`
	Domain          string               `json:"domain,omitempty"`
	Profile         string               `json:"profile,omitempty"`
	ConfigID        string               `json:"config_id,omitempty"`
	ParentTaskID    string               `json:"parent_task_id,omitempty"`
	Privacy         string               `json:"privacy,omitempty"`
	Text            string               `json:"text,omitempty"`
	ModelID         string               `json:"model_id,omitempty"`
	ProviderID      string               `json:"provider_id,omitempty"`
	ToolCallID      string               `json:"tool_call_id,omitempty"`
	ToolName        string               `json:"tool_name,omitempty"`
	Effect          Effect               `json:"effect,omitempty"`
	Code            string               `json:"code,omitempty"`
	Accepted        *bool                `json:"accepted,omitempty"`
	Messages        []providers.Message  `json:"messages,omitempty"`
	ToolCalls       []providers.ToolCall `json:"tool_calls,omitempty"`
	Usage           *providers.Usage     `json:"usage,omitempty"`
	FinishReason    string               `json:"finish_reason,omitempty"`
}

func (e Event) Validate() error {
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
	if e.Data.Compaction != nil && (e.Kind != TaskStarted || e.Data.Compaction.Validate(e.Data.ParentTaskID) != nil) {
		return errors.New("invalid context compaction")
	}
	switch e.Kind {
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

func (e Event) Encode() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	e.Time = e.Time.UTC()
	return json.Marshal(e)
}

// EventSink acknowledges an event only after its implementation accepts it.
type EventSink interface {
	Emit(context.Context, Event) error
}
