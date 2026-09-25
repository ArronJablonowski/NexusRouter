// Package diagnostics projects committed, redacted runtime events into local
// diagnostic JSON lines. It is observational and supplies no learning evidence.
package diagnostics

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

const MaxRecordBytes = 8 << 20
const MaxPageBytes = 8 << 20
const MaxPageRecords = 100

var ErrDiagnostic = errors.New("diagnostic log unavailable or invalid")

type Options struct {
	After          int64
	TaskID         string
	Limit          int
	IncludeContent bool
}

func (o Options) Validate() error {
	if o.After < 0 || len(o.TaskID) > 128 || o.Limit < 1 || o.Limit > MaxPageRecords {
		return ErrDiagnostic
	}
	return nil
}

// Origins are canonical durable task/turn starts, not model-supplied claims.
type Origins struct {
	Task runtime.Event
	Turn *runtime.Event
}

// Content is opt-in because prompts and outputs remain private session data
// even after credentials known to the runtime have been redacted.
type Content struct {
	Text      string               `json:"text,omitempty"`
	Messages  []providers.Message  `json:"messages,omitempty"`
	ToolCalls []providers.ToolCall `json:"tool_calls,omitempty"`
}

// Nullable measurements mean unavailable, rather than an invented zero.
type Record struct {
	Version       int                 `json:"version"`
	Position      int64               `json:"position"`
	Time          time.Time           `json:"time"`
	Level         string              `json:"level"`
	Kind          runtime.Kind        `json:"kind"`
	EventID       string              `json:"event_id"`
	TaskID        string              `json:"task_id"`
	SessionID     string              `json:"session_id"`
	Sequence      int64               `json:"sequence"`
	TurnID        string              `json:"turn_id,omitempty"`
	AttemptID     string              `json:"attempt_id,omitempty"`
	WorkerID      string              `json:"worker_id,omitempty"`
	Model         string              `json:"model"`
	Provider      string              `json:"provider"`
	Domain        string              `json:"domain"`
	Profile       string              `json:"profile"`
	ContextTokens *int                `json:"context_tokens"`
	TaskElapsedMS *int64              `json:"task_elapsed_ms"`
	TurnElapsedMS *int64              `json:"turn_elapsed_ms"`
	Usage         *providers.Usage    `json:"usage"`
	Resources     *resources.Snapshot `json:"resources"`
	Code          string              `json:"code,omitempty"`
	Accepted      *bool               `json:"accepted,omitempty"`
	FinishReason  string              `json:"finish_reason,omitempty"`
	ToolName      string              `json:"tool_name,omitempty"`
	ToolCallID    string              `json:"tool_call_id,omitempty"`
	Effect        runtime.Effect      `json:"effect,omitempty"`
	Route         *routing.Selection  `json:"route,omitempty"`
	Candidates    []routing.Candidate `json:"route_candidates,omitempty"`
	Policy        *routing.Policy     `json:"route_policy,omitempty"`
	EstimatedCost *float64            `json:"route_estimated_cost,omitempty"`
	TextBytes     int                 `json:"text_bytes"`
	MessageCount  int                 `json:"message_count"`
	ToolCallCount int                 `json:"tool_call_count"`
	Content       *Content            `json:"content,omitempty"`
}

type Page struct {
	Records   []Record
	NextAfter int64
	HasMore   bool
}

// Project preserves authorized I/O exactly as committed. Callers must supply
// events from the application's redacting journal, never raw provider streams.
func Project(event runtime.Event, position int64, origins Origins, includeContent bool) (Record, error) {
	if position < 1 || event.Validate() != nil || origins.Task.Validate() != nil || origins.Task.Kind != runtime.TaskStarted ||
		origins.Task.TaskID != event.TaskID || origins.Task.SessionID != event.SessionID || origins.Task.Sequence > event.Sequence {
		return Record{}, ErrDiagnostic
	}
	if origins.Turn != nil && (origins.Turn.Validate() != nil || origins.Turn.Kind != runtime.TurnStarted || origins.Turn.TaskID != event.TaskID || origins.Turn.Sequence > event.Sequence) {
		return Record{}, ErrDiagnostic
	}
	owned, err := event.Clone()
	if err != nil {
		return Record{}, ErrDiagnostic
	}
	d := owned.Data
	start := origins.Task.Data
	model, provider := start.ModelID, start.ProviderID
	if origins.Turn != nil {
		model, provider = origins.Turn.Data.ModelID, origins.Turn.Data.ProviderID
	}
	if d.ModelID != "" {
		model = d.ModelID
	}
	if d.ProviderID != "" {
		provider = d.ProviderID
	}
	level := "info"
	if event.Kind == runtime.TaskFailed || event.Kind == runtime.ErrorRecorded || d.Code == "tool_failed" {
		level = "error"
	} else if event.Kind == runtime.TaskCanceled || event.Kind == runtime.ResponseRevision || d.Accepted != nil && !*d.Accepted {
		level = "warn"
	}
	out := Record{Version: 1, Position: position, Time: event.Time, Level: level, Kind: event.Kind,
		EventID: event.ID, TaskID: event.TaskID, SessionID: event.SessionID, Sequence: event.Sequence,
		TurnID: event.TurnID, AttemptID: event.AttemptID, WorkerID: event.WorkerID,
		Model: model, Provider: provider, Domain: start.Domain, Profile: start.Profile,
		TaskElapsedMS: elapsed(origins.Task.Time, event.Time), Usage: d.Usage, Resources: d.Resources,
		Code: d.Code, Accepted: d.Accepted, FinishReason: d.FinishReason, ToolName: d.ToolName,
		ToolCallID: d.ToolCallID, Effect: d.Effect, Route: d.Route, Candidates: d.RouteCandidates,
		Policy: d.RoutePolicy, EstimatedCost: d.RouteEstimatedCost,
		TextBytes: len(d.Text), MessageCount: len(d.Messages), ToolCallCount: len(d.ToolCalls)}
	if start.ContextTokens > 0 {
		tokens := start.ContextTokens
		out.ContextTokens = &tokens
	}
	if origins.Turn != nil && event.TurnID == origins.Turn.TurnID && event.AttemptID == origins.Turn.AttemptID {
		out.TurnElapsedMS = elapsed(origins.Turn.Time, event.Time)
	}
	if d.Domain != "" {
		out.Domain = d.Domain
	}
	if d.Profile != "" {
		out.Profile = d.Profile
	}
	if includeContent && (d.Text != "" || len(d.Messages) > 0 || len(d.ToolCalls) > 0) {
		out.Content = &Content{Text: d.Text, Messages: d.Messages, ToolCalls: d.ToolCalls}
	}
	body, err := json.Marshal(out)
	if err != nil || len(body) > MaxRecordBytes {
		return Record{}, ErrDiagnostic
	}
	return out, nil
}

func elapsed(start, end time.Time) *int64 {
	if start.IsZero() || end.Before(start) {
		return nil
	}
	value := end.Sub(start).Milliseconds()
	return &value
}
