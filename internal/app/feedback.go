package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// RecordFeedback is an operator-only adapter, never registered as a model tool.
// Cost is supplied explicitly by the operator, not inferred from missing usage.
// The immutable final-attempt record gives repeated identical feedback no extra
// weight; conflicting feedback requires a future supersession workflow.
func RecordFeedback(ctx context.Context, path, task string, accepted bool, cost float64) error {
	if task == "" || len(task) > 128 || math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return ErrAdmission
	}
	ro, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return errors.New("feedback database unavailable")
	}
	defer ro.Close()
	snapshot, err := sessions.Replay(ctx, ro, task)
	if err != nil || snapshot.State != "completed" || snapshot.UncertainEffects || snapshot.InterruptedTurn || len(snapshot.Pending) > 0 {
		return ErrAdmission
	}
	key := routing.Key{Domain: "general", Profile: "default"}
	var start, end runtime.Event
	var sequence int64
	for pages := 0; pages < 1000; pages++ {
		events, err := ro.Read(ctx, task, sequence, 256)
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
	latency := end.Time.Sub(start.Time)
	if latency < 0 {
		return ErrAdmission
	}
	hash := sha256.Sum256([]byte("user-feedback:" + task + ":" + start.AttemptID))
	id := hex.EncodeToString(hash[:])
	record := evaluation.Record{Version: 1, ID: id, TaskID: task, AttemptID: start.AttemptID, Key: key, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: id, Passed: accepted}}, ExecutionSucceeded: true, Latency: latency, Cost: cost, Time: end.Time.UTC()}
	if record.Time.IsZero() {
		return ErrAdmission
	}
	if err := record.Validate(); err != nil {
		return ErrAdmission
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		return errors.New("feedback database unavailable")
	}
	defer db.Close()
	return db.RecordEvaluation(ctx, record)
}
