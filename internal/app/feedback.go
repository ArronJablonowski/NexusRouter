package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// RecordFeedback is an operator-only adapter, never registered as a model tool.
// Cost is supplied explicitly by the operator, not inferred from missing usage.
// The immutable final-attempt record gives repeated identical feedback no extra
// weight; conflicting feedback requires a future supersession workflow.
func RecordFeedback(ctx context.Context, path, task string, accepted bool, cost float64) error {
	// Keep the standalone command from creating a database when the configured
	// path is absent. The daemon path below receives its already-open store.
	ro, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return errors.New("feedback database unavailable")
	}
	if err = ro.Close(); err != nil {
		return errors.New("feedback database unavailable")
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		return errors.New("feedback database unavailable")
	}
	defer db.Close()
	return RecordFeedbackStore(ctx, db, task, accepted, cost)
}

// RecordFeedbackStore records feedback through the daemon's existing store.
// Opening a second writable Store inside the HTTP handler can contend with the
// dispatcher long enough to expire claim queries and degrade its supervisor.
func RecordFeedbackStore(ctx context.Context, db *telemetry.Store, task string, accepted bool, cost float64) error {
	if task == "" || len(task) > 128 || math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return ErrAdmission
	}
	if db == nil {
		return errors.New("feedback database unavailable")
	}
	snapshot, err := sessions.Replay(ctx, db, task)
	if err != nil || snapshot.State != "completed" || snapshot.UncertainEffects || snapshot.InterruptedTurn || len(snapshot.Pending) > 0 {
		return ErrAdmission
	}
	key := routing.Key{Domain: "general", Profile: "default"}
	var start, end runtime.Event
	var taskStart time.Time
	contextTokens := 0
	var sequence int64
	for pages := 0; pages < 1000; pages++ {
		events, err := db.Read(ctx, task, sequence, 256)
		if err != nil {
			return errors.New("feedback history unavailable")
		}
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			sequence = e.Sequence
			switch e.Kind {
			case runtime.TaskStarted:
				taskStart = e.Time
				contextTokens = e.Data.ContextTokens
				if e.Data.Domain != "" {
					key.Domain = e.Data.Domain
				}
				if e.Data.Profile != "" {
					key.Profile = e.Data.Profile
				}
			case runtime.RouteSelected:
				key.Domain, key.Profile = e.Data.Domain, e.Data.Profile
			case runtime.TurnStarted:
				start = e
				end = runtime.Event{}
				key.Model, key.Provider = e.Data.ModelID, e.Data.ProviderID
			case runtime.TurnCompleted:
				end = e
			}
		}
		if len(events) < 256 {
			break
		}
		if pages == 999 {
			return ErrAdmission
		}
	}
	if start.AttemptID == "" || end.AttemptID != start.AttemptID {
		return ErrAdmission
	}
	// Feedback judges the completed task, including tools and earlier turns.
	// Measuring only its final answer systematically rewards long agent loops.
	if taskStart.IsZero() || taskStart.After(start.Time) {
		return ErrAdmission
	}
	latency := end.Time.Sub(taskStart)
	if latency < 0 {
		return ErrAdmission
	}
	hash := sha256.Sum256([]byte("user-feedback:" + task + ":" + start.AttemptID))
	id := hex.EncodeToString(hash[:])
	record := evaluation.Record{Version: 1, ID: id, TaskID: task, AttemptID: start.AttemptID, Key: key, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: id, Passed: accepted}}, ExecutionSucceeded: true, Latency: latency, ContextTokens: contextTokens, Cost: cost, Time: end.Time.UTC()}
	if record.Time.IsZero() {
		return ErrAdmission
	}
	if err := record.Validate(); err != nil {
		return ErrAdmission
	}
	// Preserve immutable historical measurements on identical retries across
	// this correction. New tasks receive whole-task latency; old records are
	// not silently rewritten or turned into duplicate samples.
	history, historyErr := db.EvaluationHistory(ctx, task, start.AttemptID)
	if historyErr != nil && !errors.Is(historyErr, sql.ErrNoRows) {
		return historyErr
	}
	for _, prior := range history {
		if prior.ID == record.ID {
			record.Latency = prior.Latency
			break
		}
	}
	return db.RecordEvaluation(ctx, record)
}
