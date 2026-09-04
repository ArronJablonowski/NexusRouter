// Package runtime defines the provider-neutral protocol shared by all adapters.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"darwinrouter/providers"
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
	ParentTaskID string               `json:"parent_task_id,omitempty"`
	Privacy      string               `json:"privacy,omitempty"`
	Text         string               `json:"text,omitempty"`
	ModelID      string               `json:"model_id,omitempty"`
	ProviderID   string               `json:"provider_id,omitempty"`
	ToolCallID   string               `json:"tool_call_id,omitempty"`
	ToolName     string               `json:"tool_name,omitempty"`
	Effect       Effect               `json:"effect,omitempty"`
	Code         string               `json:"code,omitempty"`
	Accepted     *bool                `json:"accepted,omitempty"`
	Messages     []providers.Message  `json:"messages,omitempty"`
	ToolCalls    []providers.ToolCall `json:"tool_calls,omitempty"`
	Usage        *providers.Usage     `json:"usage,omitempty"`
	FinishReason string               `json:"finish_reason,omitempty"`
}

func (e Event) Validate() error {
	if e.Version != 1 || e.ID == "" || e.TaskID == "" || e.SessionID == "" || e.CorrelationID == "" || e.Sequence < 1 || e.Time.IsZero() {
		return errors.New("invalid event envelope")
	}
	switch e.Kind {
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
