package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/traces"
)

var errTraces = errors.New("traces unavailable")

type traceTask struct {
	id, state string
}

type tracePairKey struct {
	kind, turn, attempt, call string
}

type traceStart struct {
	at       time.Time
	toolName string
}

// Traces reconstructs bounded recent task and skill-generation operations
// inside one SQLite read snapshot. Running task roots end at ObservedAt and
// include only durably completed child operations. Only pairing fields and a
// bounded top-level submission time are selected; event bodies, durable IDs,
// model/provider/tool/skill names and session content never enter the result.
func (s *Store) Traces(ctx context.Context, limit int) (traces.Snapshot, error) {
	if limit < 1 || limit > traces.MaxTraces {
		return traces.Snapshot{}, errTraces
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return traces.Snapshot{}, errTraces
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT task_id,state FROM task_heads WHERE state IN ('running','completed','failed','canceled') ORDER BY rowid DESC LIMIT ?`, limit)
	if err != nil {
		return traces.Snapshot{}, errTraces
	}
	tasks := make([]traceTask, 0, limit)
	for rows.Next() {
		var task traceTask
		if rows.Scan(&task.id, &task.state) != nil || len(task.id) == 0 || len(task.id) > 128 {
			rows.Close()
			return traces.Snapshot{}, errTraces
		}
		tasks = append(tasks, task)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return traces.Snapshot{}, errTraces
	}
	out := traces.Snapshot{Version: traces.SnapshotVersion, ObservedAt: time.Now().UTC(), Traces: make([]traces.Trace, 0, len(tasks))}
	for _, task := range tasks {
		trace, readErr := readTaskTrace(ctx, tx, task, out.ObservedAt)
		if readErr != nil {
			return traces.Snapshot{}, errTraces
		}
		out.Traces = append(out.Traces, trace)
	}
	generation, err := readSkillGenerationTraces(ctx, tx, limit, out.ObservedAt)
	if err != nil {
		return traces.Snapshot{}, errTraces
	}
	out.Traces = append(out.Traces, generation...)
	sort.SliceStable(out.Traces, func(i, j int) bool {
		left, right := out.Traces[i].Spans[0], out.Traces[j].Spans[0]
		if left.EndedAt.Equal(right.EndedAt) {
			return left.Name < right.Name
		}
		return left.EndedAt.After(right.EndedAt)
	})
	if len(out.Traces) > limit {
		out.Traces = out.Traces[:limit]
	}
	if out.Validate() != nil || tx.Commit() != nil {
		return traces.Snapshot{}, errTraces
	}
	return out, nil
}

func readTaskTrace(ctx context.Context, tx *sql.Tx, task traceTask, observedAt time.Time) (traces.Trace, error) {
	rows, err := tx.QueryContext(ctx, `WITH trace_events AS (
	 SELECT e.*,
	  max(sequence) FILTER (WHERE json_extract(body,'$.kind')='worker.heartbeat') OVER (
	   PARTITION BY json_extract(body,'$.worker_id')) AS worker_heartbeat_max,
	  min(sequence) FILTER (WHERE json_extract(body,'$.kind')='model.delta') OVER (
	   PARTITION BY json_extract(body,'$.turn_id'),COALESCE(json_extract(body,'$.attempt_id'),'')) AS model_delta_first,
	  max(sequence) FILTER (WHERE json_extract(body,'$.kind')='model.delta') OVER (
	   PARTITION BY json_extract(body,'$.turn_id'),COALESCE(json_extract(body,'$.attempt_id'),'')) AS model_delta_last
	 FROM events AS e INDEXED BY events_task_kind
	 WHERE task_id=? AND json_extract(body,'$.kind') IN
	 ('task.started','task.completed','task.failed','task.canceled','turn.started','turn.completed','model.delta','tool.started','tool.completed',
	  'worker.started','worker.heartbeat','worker.completed','route.selected','evaluation.recorded','error.recorded','steering.applied','context.compacted')
	)
	SELECT sequence,json_extract(body,'$.kind'),
	 CASE WHEN json_type(body,'$.turn_id')='text' AND length(CAST(json_extract(body,'$.turn_id') AS BLOB)) BETWEEN 1 AND 256 THEN json_extract(body,'$.turn_id') ELSE '' END,
	 CASE WHEN json_type(body,'$.attempt_id') IS NULL THEN '' WHEN json_type(body,'$.attempt_id')='text' AND length(CAST(json_extract(body,'$.attempt_id') AS BLOB))<=256 THEN json_extract(body,'$.attempt_id') ELSE '' END,
	 CASE WHEN json_type(body,'$.data.tool_call_id') IS NULL THEN '' WHEN json_type(body,'$.data.tool_call_id')='text' AND length(CAST(json_extract(body,'$.data.tool_call_id') AS BLOB)) BETWEEN 1 AND 256 THEN json_extract(body,'$.data.tool_call_id') ELSE '' END,
	 CASE WHEN json_type(body,'$.data.tool_name') IS NULL THEN '' WHEN json_type(body,'$.data.tool_name')='text' AND length(CAST(json_extract(body,'$.data.tool_name') AS BLOB)) BETWEEN 1 AND 256 THEN json_extract(body,'$.data.tool_name') ELSE '' END,
	 CASE WHEN json_type(body,'$.worker_id') IS NULL THEN '' WHEN json_type(body,'$.worker_id')='text' AND length(CAST(json_extract(body,'$.worker_id') AS BLOB)) BETWEEN 1 AND 256 THEN json_extract(body,'$.worker_id') ELSE '' END,
	 CASE WHEN json_type(body,'$.time')='text' AND length(CAST(json_extract(body,'$.time') AS BLOB)) BETWEEN 1 AND 40 THEN json_extract(body,'$.time') ELSE '' END,
	 CASE WHEN json_type(body,'$.data.retry_of_task_id')='text' AND length(CAST(json_extract(body,'$.data.retry_of_task_id') AS BLOB)) BETWEEN 1 AND 128 THEN 1 ELSE 0 END,
	 CASE WHEN json_type(body,'$.data.compaction')='object' THEN 1 ELSE 0 END,
	 CASE WHEN json_type(body,'$.data.skill_context')='object' THEN 1 ELSE 0 END,
	 CASE WHEN json_extract(body,'$.data.route.Explored')=1 THEN 1 ELSE 0 END,
	 CASE WHEN json_type(body,'$.data.accepted')='true' THEN 1 WHEN json_type(body,'$.data.accepted')='false' THEN 0 ELSE -1 END,
	 COALESCE((SELECT sum(DISTINCT CASE r.value WHEN 'mode' THEN 1 WHEN 'privacy' THEN 2 WHEN 'health' THEN 4 WHEN 'policy' THEN 8 WHEN 'credential' THEN 16 WHEN 'capacity' THEN 32 WHEN 'context' THEN 64 WHEN 'budget' THEN 128 WHEN 'capability' THEN 256 ELSE 512 END)
	  FROM json_each(json_extract(body,'$.data.route.Excluded')) AS x,json_each(json_extract(x.value,'$.Reasons')) AS r),0),
	 CASE
	  WHEN json_extract(body,'$.kind')<>'task.started' OR json_type(body,'$.data.submission_id') IS NULL THEN ''
	  WHEN json_type(body,'$.data.submission_id')='text'
	   AND length(CAST(json_extract(body,'$.data.submission_id') AS BLOB)) BETWEEN 1 AND 128
	   AND json_type(body,'$.data.parent_task_id') IS NULL
	   AND json_type(body,'$.data.retry_of_task_id') IS NULL
	   AND (SELECT count(*) FROM submissions s WHERE s.id=json_extract(body,'$.data.submission_id'))=1
	  THEN COALESCE((SELECT CASE WHEN length(CAST(s.created_at AS BLOB)) BETWEEN 1 AND 64 THEN s.created_at END FROM submissions s WHERE s.id=json_extract(body,'$.data.submission_id')),'!invalid!')
	  ELSE '!invalid!'
	 END,
	 CASE
	  WHEN json_extract(body,'$.kind')<>'tool.completed' THEN ''
	  WHEN json_extract(body,'$.data.effect') IN ('none','confirmed','uncertain') THEN json_extract(body,'$.data.effect')
	  ELSE '!invalid!'
	 END,
	 CASE
	  WHEN json_extract(body,'$.kind')<>'task.started' OR json_type(body,'$.data.resources') IS NULL THEN -1
	  WHEN json_type(body,'$.data.resources')<>'object' THEN -2
	  WHEN json_type(body,'$.data.resources.ThermalPressure') IS NULL THEN -1
	  WHEN json_type(body,'$.data.resources.ThermalPressure')='true' THEN 1
	  WHEN json_type(body,'$.data.resources.ThermalPressure')='false' THEN 0
	  ELSE -2
	 END,
	 CASE
	  WHEN json_extract(body,'$.kind')<>'task.started' OR json_type(body,'$.data.resources') IS NULL THEN -1
	  WHEN json_type(body,'$.data.resources')<>'object' THEN -2
	  WHEN json_type(body,'$.data.resources.swap_pressure') IS NULL THEN -1
	  WHEN json_type(body,'$.data.resources.swap_pressure')='true' THEN 1
	  WHEN json_type(body,'$.data.resources.swap_pressure')='false' THEN 0
	  ELSE -2
	 END,
	 CASE
	  WHEN json_extract(body,'$.kind')<>'model.delta' THEN 0
	  WHEN json_type(body,'$.turn_id')<>'text' OR length(CAST(json_extract(body,'$.turn_id') AS BLOB)) NOT BETWEEN 1 AND 256
	   OR (json_type(body,'$.attempt_id') IS NOT NULL AND (json_type(body,'$.attempt_id')<>'text' OR length(CAST(json_extract(body,'$.attempt_id') AS BLOB))>256)) THEN -1
	  ELSE CASE WHEN sequence=model_delta_first THEN 1 ELSE 0 END
	   + CASE WHEN sequence=model_delta_last THEN 2 ELSE 0 END
	 END
	 FROM trace_events AS e WHERE
	 (json_extract(body,'$.kind')<>'worker.heartbeat'
	  OR json_type(body,'$.worker_id') IS NULL OR json_type(body,'$.worker_id')<>'text'
	  OR length(CAST(json_extract(body,'$.worker_id') AS BLOB)) NOT BETWEEN 1 AND 256
	  OR sequence=worker_heartbeat_max)
	 AND (json_extract(body,'$.kind')<>'model.delta'
	  OR json_type(body,'$.turn_id') IS NULL OR json_type(body,'$.turn_id')<>'text'
	  OR length(CAST(json_extract(body,'$.turn_id') AS BLOB)) NOT BETWEEN 1 AND 256
	  OR (json_type(body,'$.attempt_id') IS NOT NULL AND (json_type(body,'$.attempt_id')<>'text' OR length(CAST(json_extract(body,'$.attempt_id') AS BLOB))>256))
	  OR sequence=model_delta_first OR sequence=model_delta_last)
	 ORDER BY sequence LIMIT 514`, task.id)
	if err != nil {
		return traces.Trace{}, errTraces
	}
	defer rows.Close()
	pending := map[tracePairKey]traceStart{}
	modelStarts := map[tracePairKey]time.Time{}
	children := make([]traces.Span, 0)
	var rootStart, rootEnd time.Time
	terminalSeen := false
	count := 0
	for rows.Next() {
		count++
		if count > 513 {
			return traces.Trace{}, errTraces
		}
		var sequence int64
		var kind, turn, attempt, call, toolName, worker, encodedTime, queuedAt, toolEffect string
		var retry, compaction, skillContext, explored, accepted, routeConstraints, thermalPressure, swapPressure, modelEdge int
		if rows.Scan(&sequence, &kind, &turn, &attempt, &call, &toolName, &worker, &encodedTime, &retry, &compaction, &skillContext, &explored, &accepted, &routeConstraints, &queuedAt, &toolEffect, &thermalPressure, &swapPressure, &modelEdge) != nil || sequence < 1 {
			return traces.Trace{}, errTraces
		}
		at, valid := operationMetricTime(encodedTime, observedAt)
		if !valid {
			return traces.Trace{}, errTraces
		}
		switch kind {
		case "task.started":
			if !rootStart.IsZero() || thermalPressure < -1 || thermalPressure > 1 || swapPressure < -1 || swapPressure > 1 {
				return traces.Trace{}, errTraces
			}
			rootStart = at
			if thermalPressure == 1 {
				children = append(children, traceInstant("resource_pressure", "thermal", at))
			}
			if swapPressure == 1 {
				children = append(children, traceInstant("resource_pressure", "swap", at))
			}
			if queuedAt != "" {
				created, parseErr := time.Parse(time.RFC3339Nano, queuedAt)
				if parseErr != nil || created.Location() != time.UTC || created.After(at) {
					return traces.Trace{}, errTraces
				}
				children = append(children, traceInstant("queue_residency", queueResidencyBucket(at.Sub(created)), at))
			}
			if retry == 1 {
				children = append(children, traceInstant("fallback", "selected", at))
			}
			if compaction == 1 {
				children = append(children, traceInstant("compaction", "applied", at))
			}
			if skillContext == 1 {
				children = append(children, traceInstant("skill_context", "loaded", at))
			}
		case "task.completed", "task.failed", "task.canceled":
			if terminalSeen || task.state == "running" || kind != "task."+task.state {
				return traces.Trace{}, errTraces
			}
			terminalSeen = true
			rootEnd = at
		case "model.delta":
			key := tracePairKey{kind: "provider", turn: turn, attempt: attempt}
			started, active := pending[key]
			if !active || turn == "" || at.Before(started.at) || modelEdge < 1 || modelEdge > 3 {
				return traces.Trace{}, errTraces
			}
			switch modelEdge {
			case 1:
				if _, exists := modelStarts[key]; exists {
					return traces.Trace{}, errTraces
				}
				modelStarts[key] = at
			case 2:
				first, exists := modelStarts[key]
				if !exists || at.Before(first) {
					return traces.Trace{}, errTraces
				}
				delete(modelStarts, key)
				children = append(children, traces.Span{Name: "model_output", Outcome: "observed", Parent: 0, StartedAt: first, EndedAt: at})
			case 3:
				children = append(children, traceInstant("model_output", "observed", at))
			}
		case "turn.started", "turn.completed", "tool.started", "tool.completed":
			group := "provider"
			start := kind == "turn.started" || kind == "tool.started"
			pairable := turn != ""
			if kind == "tool.started" || kind == "tool.completed" {
				group, pairable = "tool", pairable && call != "" && toolName != ""
			} else {
				pairable = pairable && call == "" && toolName == ""
			}
			if !pairable {
				continue
			}
			key := tracePairKey{kind: group, turn: turn, attempt: attempt, call: call}
			if start {
				if _, exists := pending[key]; exists {
					return traces.Trace{}, errTraces
				}
				pending[key] = traceStart{at: at, toolName: toolName}
				continue
			}
			started, exists := pending[key]
			if !exists {
				continue
			}
			delete(pending, key)
			if started.toolName != toolName || at.Before(started.at) {
				return traces.Trace{}, errTraces
			}
			children = append(children, traces.Span{Name: group, Outcome: "completed", Parent: 0, StartedAt: started.at, EndedAt: at})
			if group == "tool" {
				if toolEffect == "" || toolEffect == "!invalid!" {
					return traces.Trace{}, errTraces
				}
				children = append(children, traceInstant("tool_effect", toolEffect, at))
			}
		case "worker.started", "worker.heartbeat", "worker.completed":
			if worker == "" {
				return traces.Trace{}, errTraces
			}
			key := tracePairKey{kind: "worker", call: worker}
			if kind == "worker.started" {
				if _, exists := pending[key]; exists {
					return traces.Trace{}, errTraces
				}
				pending[key] = traceStart{at: at}
				continue
			}
			started, exists := pending[key]
			if kind == "worker.heartbeat" {
				if !exists || at.Before(started.at) {
					return traces.Trace{}, errTraces
				}
				children = append(children, traceInstant("worker_heartbeat", "observed", at))
				continue
			}
			if !exists {
				continue
			}
			delete(pending, key)
			if at.Before(started.at) {
				return traces.Trace{}, errTraces
			}
			children = append(children, traces.Span{Name: "worker", Outcome: "completed", Parent: 0, StartedAt: started.at, EndedAt: at})
		case "route.selected":
			if routeConstraints < 0 || routeConstraints > 511 {
				return traces.Trace{}, errTraces
			}
			outcome := "selected"
			if explored == 1 {
				outcome = "explored"
			}
			children = append(children, traceInstant("route", outcome, at))
			for bit, reason := range []string{"mode", "privacy", "health", "policy", "credential", "capacity", "context", "budget", "capability"} {
				if routeConstraints&(1<<bit) != 0 {
					children = append(children, traceInstant("route_constraint", reason, at))
				}
			}
		case "evaluation.recorded":
			if accepted != 0 && accepted != 1 {
				return traces.Trace{}, errTraces
			}
			outcome := "rejected"
			if accepted == 1 {
				outcome = "accepted"
			}
			children = append(children, traceInstant("evaluation", outcome, at))
		case "error.recorded":
			children = append(children, traceInstant("error", "recorded", at))
		case "steering.applied":
			children = append(children, traceInstant("steering", "applied", at))
		case "context.compacted":
			children = append(children, traceInstant("compaction", "applied", at))
		default:
			return traces.Trace{}, errTraces
		}
	}
	if task.state == "running" {
		if terminalSeen {
			return traces.Trace{}, errTraces
		}
		rootEnd = observedAt
	}
	if rows.Err() != nil || count > 512 || len(modelStarts) != 0 || rootStart.IsZero() || rootEnd.IsZero() || rootEnd.Before(rootStart) {
		return traces.Trace{}, errTraces
	}
	if rows.Close() != nil {
		return traces.Trace{}, errTraces
	}
	leaseObservations, err := readTaskLeaseTrace(ctx, tx, task.id, rootEnd, observedAt)
	if err != nil {
		return traces.Trace{}, errTraces
	}
	children = append(children, leaseObservations...)
	if task.state != "running" {
		fitnessObservations, err := readTaskFitnessTrace(ctx, tx, task.id, rootEnd, observedAt)
		if err != nil {
			return traces.Trace{}, errTraces
		}
		children = append(children, fitnessObservations...)
	}
	sort.SliceStable(children, func(i, j int) bool { return children[i].StartedAt.Before(children[j].StartedAt) })
	spans := make([]traces.Span, 1, len(children)+1)
	spans[0] = traces.Span{Name: "task", Outcome: task.state, Parent: -1, StartedAt: rootStart, EndedAt: rootEnd}
	spans = append(spans, children...)
	return traces.Trace{Spans: spans}, nil
}

func queueResidencyBucket(wait time.Duration) string {
	switch {
	case wait < time.Second:
		return "lt_1s"
	case wait < 10*time.Second:
		return "lt_10s"
	case wait < time.Minute:
		return "lt_1m"
	case wait < 5*time.Minute:
		return "lt_5m"
	case wait < 30*time.Minute:
		return "lt_30m"
	case wait < time.Hour:
		return "lt_1h"
	default:
		return "gte_1h"
	}
}

func traceInstant(name, outcome string, at time.Time) traces.Span {
	return traces.Span{Name: name, Outcome: outcome, Parent: 0, StartedAt: at, EndedAt: at}
}
