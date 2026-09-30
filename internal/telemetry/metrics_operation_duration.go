package telemetry

import (
	"context"
	"database/sql"
	"math"
	"time"

	"github.com/ArronJablonowski/NexusRouter/metrics"
)

type operationDurationKey struct {
	kind, turn, attempt, call string
}

type operationDurationStart struct {
	at       time.Time
	valid    bool
	toolName string
}

// readOperationDuration extracts only bounded timing/pairing fields. Private
// event payloads and provider/model/tool/task identities never enter the public
// snapshot. Ordering bounds pending state to one task journal at a time.
func readOperationDuration(ctx context.Context, tx *sql.Tx, snapshot *metrics.Snapshot) error {
	if snapshot.OperationDuration == nil {
		return errMetrics
	}
	rows, err := tx.QueryContext(ctx, `SELECT
 CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id ELSE '' END,
 sequence,
 json_extract(body,'$.kind'),
	CASE WHEN json_type(body,'$.turn_id')='text' AND length(CAST(json_extract(body,'$.turn_id') AS BLOB)) BETWEEN 1 AND 256 THEN json_extract(body,'$.turn_id') ELSE '' END,
 CASE WHEN json_type(body,'$.attempt_id') IS NULL THEN '' WHEN json_type(body,'$.attempt_id')='text' AND length(CAST(json_extract(body,'$.attempt_id') AS BLOB))<=256 THEN json_extract(body,'$.attempt_id') ELSE '' END,
 CASE WHEN json_type(body,'$.data.tool_call_id') IS NULL THEN '' WHEN json_type(body,'$.data.tool_call_id')='text' AND length(CAST(json_extract(body,'$.data.tool_call_id') AS BLOB)) BETWEEN 1 AND 256 THEN json_extract(body,'$.data.tool_call_id') ELSE '' END,
 CASE WHEN json_type(body,'$.data.tool_name') IS NULL THEN '' WHEN json_type(body,'$.data.tool_name')='text' AND length(CAST(json_extract(body,'$.data.tool_name') AS BLOB)) BETWEEN 1 AND 256 THEN json_extract(body,'$.data.tool_name') ELSE '' END,
 CASE WHEN json_type(body,'$.time')='text' AND length(CAST(json_extract(body,'$.time') AS BLOB)) BETWEEN 1 AND 40 THEN json_extract(body,'$.time') ELSE '' END
 FROM events WHERE json_extract(body,'$.kind') IN ('turn.started','turn.completed','tool.started','tool.completed')
 ORDER BY task_id,sequence`)
	if err != nil {
		return errMetrics
	}
	defer rows.Close()
	pending := map[operationDurationKey]operationDurationStart{}
	currentTask := ""
	eventsInTask := 0
	flush := func() {
		for key := range pending {
			group := operationDurationGroup(snapshot, key.kind)
			group.Unavailable[1].Value++
		}
		clear(pending)
	}
	for rows.Next() {
		var task, kind, turn, attempt, call, toolName, encodedTime string
		var sequence int64
		if rows.Scan(&task, &sequence, &kind, &turn, &attempt, &call, &toolName, &encodedTime) != nil {
			return errMetrics
		}
		if task != currentTask {
			flush()
			eventsInTask = 0
		}
		currentTask = task
		eventsInTask++
		if eventsInTask > 10000 {
			return errMetrics
		}
		groupKind, start := "", false
		switch kind {
		case "turn.started":
			groupKind, start = "provider", true
		case "turn.completed":
			groupKind = "provider"
		case "tool.started":
			groupKind, start = "tool", true
		case "tool.completed":
			groupKind = "tool"
		default:
			return errMetrics
		}
		pairable := task != "" && sequence > 0 && turn != ""
		if groupKind == "provider" {
			pairable = pairable && call == "" && toolName == ""
		} else {
			pairable = pairable && call != "" && toolName != ""
		}
		if !pairable {
			group := operationDurationGroup(snapshot, groupKind)
			if start {
				group.Unavailable[1].Value++
			} else {
				group.Unavailable[0].Value++
			}
			continue
		}
		key := operationDurationKey{kind: groupKind, turn: turn, attempt: attempt, call: call}
		at, valid := operationMetricTime(encodedTime, snapshot.ObservedAt)
		if valid && at.Before(snapshot.OperationDuration.StartedAt) {
			snapshot.OperationDuration.StartedAt = at
		}
		if start {
			if _, exists := pending[key]; exists {
				return errMetrics
			}
			pending[key] = operationDurationStart{at: at, valid: valid, toolName: toolName}
			continue
		}
		started, exists := pending[key]
		if !exists {
			operationDurationGroup(snapshot, groupKind).Unavailable[0].Value++
			continue
		}
		delete(pending, key)
		if started.toolName != toolName {
			return errMetrics
		}
		group := operationDurationGroup(snapshot, groupKind)
		duration, durationOK := operationMetricDuration(started.at, at, started.valid && valid)
		if !durationOK {
			group.Unavailable[2].Value++
			continue
		}
		recordOperationDuration(group, duration)
	}
	if rows.Err() != nil {
		return errMetrics
	}
	flush()
	return nil
}

func operationDurationGroup(snapshot *metrics.Snapshot, kind string) *metrics.DurationGroup {
	if kind == "provider" {
		return &snapshot.OperationDuration.Groups[0]
	}
	return &snapshot.OperationDuration.Groups[1]
}

func operationMetricTime(value string, observedAt time.Time) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || at.Before(time.Unix(0, 0)) || at.After(observedAt) || value != at.UTC().Format(time.RFC3339Nano) {
		return time.Time{}, false
	}
	return at, true
}

func operationMetricDuration(start, end time.Time, valid bool) (time.Duration, bool) {
	if !valid || end.Before(start) {
		return 0, false
	}
	seconds := end.Unix() - start.Unix()
	nanos := int64(end.Nanosecond() - start.Nanosecond())
	if nanos < 0 {
		seconds--
		nanos += int64(time.Second)
	}
	maximum := int64(math.MaxInt64)
	if seconds < 0 || seconds > maximum/int64(time.Second) || seconds == maximum/int64(time.Second) && nanos > maximum%int64(time.Second) {
		return 0, false
	}
	return time.Duration(seconds*int64(time.Second) + nanos), true
}

func recordOperationDuration(group *metrics.DurationGroup, duration time.Duration) {
	group.Count++
	group.SumSeconds += duration.Seconds()
	index := len(group.BucketCounts) - 1
	for i, bound := range metrics.TaskDurationBounds() {
		if duration <= bound {
			index = i
			break
		}
	}
	group.BucketCounts[index]++
}
