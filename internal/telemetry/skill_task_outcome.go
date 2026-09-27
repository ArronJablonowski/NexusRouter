package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// SkillTaskOutcome reads one coherent, bounded observation. It never loads the
// current skill catalog: activation changes cannot relabel historical exposure.
// Missing evaluations and legacy attribution are unknown, not negative evidence.
func (s *Store) SkillTaskOutcome(ctx context.Context, task string) (out skills.TaskOutcome, err error) {
	if ctx == nil || s == nil || s.db == nil || !sessions.ValidEventPageID(task) {
		return out, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			out, err = skills.TaskOutcome{}, ctx.Err()
		} else if err != nil {
			out = skills.TaskOutcome{}
		}
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out, err = skillTaskOutcome(ctx, tx, task)
	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

func skillTaskOutcome(ctx context.Context, tx *sql.Tx, task string) (out skills.TaskOutcome, err error) {
	var events []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, task, &events)
	if err != nil {
		return out, err
	}
	out = skills.TaskOutcome{Version: 1, TaskID: task, SessionID: snapshot.SessionID, State: snapshot.State, Privacy: snapshot.Privacy, Sequence: snapshot.Sequence, SkillContext: events[0].Data.SkillContext, OutputChecks: []skills.TaskOutputCheck{}}
	out.ParentTaskID, out.RetryOfTaskID = snapshot.ParentTaskID, snapshot.RetryOfTaskID
	key := routing.Key{Domain: "general", Profile: "default"}
	var start, end runtime.Event
	for _, e := range events {
		switch e.Kind {
		case runtime.TaskStarted, runtime.RouteSelected:
			if e.Data.Domain != "" {
				key.Domain = e.Data.Domain
			}
			if e.Data.Profile != "" {
				key.Profile = e.Data.Profile
			}
		case runtime.TurnStarted:
			start, end = e, runtime.Event{}
			key.Model, key.Provider = e.Data.ModelID, e.Data.ProviderID
			owned := key
			out.Key = &owned
			out.AttemptID = e.AttemptID
		case runtime.TurnCompleted:
			end = e
		}
	}
	if out.Key != nil {
		if err = skillTaskQuality(ctx, tx, &out); err != nil {
			return out, err
		}
		seen := map[string]bool{}
		for _, e := range events {
			if e.Sequence <= start.Sequence {
				continue
			}
			if e.Kind == runtime.SteeringApplied {
				out.OutputChecks = []skills.TaskOutputCheck{}
				break
			}
			if e.Kind != runtime.EvaluationRecorded || (e.Data.Code != "deterministic.nonempty_text.v1" && e.Data.Code != "deterministic.go_syntax.v1") {
				continue
			}
			if end.Kind != runtime.TurnCompleted || end.AttemptID != start.AttemptID || end.TurnID != start.TurnID || len(end.Data.ToolCalls) > 0 || e.Sequence <= end.Sequence || e.AttemptID != start.AttemptID || e.TurnID != start.TurnID || e.Data.Accepted == nil || e.Data.ModelID != out.Key.Model || e.Data.ProviderID != out.Key.Provider || seen[e.Data.Code] {
				return out, skills.ErrInvalid
			}
			domain, profile := e.Data.Domain, e.Data.Profile
			if domain == "" {
				domain = "general"
			}
			if profile == "" {
				profile = "default"
			}
			if domain != out.Key.Domain || profile != out.Key.Profile {
				return out, skills.ErrInvalid
			}
			passed := strings.TrimSpace(end.Data.Text) != ""
			if e.Data.Code == "deterministic.go_syntax.v1" {
				if events[0].Data.Validation != "go_source" {
					return out, skills.ErrInvalid
				}
				passed = evaluation.GoSourceValid(end.Data.Text)
			}
			if passed != *e.Data.Accepted {
				return out, skills.ErrInvalid
			}
			seen[e.Data.Code] = true
			out.OutputChecks = append(out.OutputChecks, skills.TaskOutputCheck{EventID: e.ID, Code: e.Data.Code, Passed: *e.Data.Accepted})
		}
	}
	if out.Validate() != nil {
		return out, skills.ErrInvalid
	}
	return out, nil
}

func skillTaskQuality(ctx context.Context, tx *sql.Tx, out *skills.TaskOutcome) error {
	if err := preflightEvaluationKey(ctx, tx, out.TaskID, out.AttemptID, *out.Key, 1024); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM evaluations WHERE task_id=? AND attempt_id=?", out.TaskID, out.AttemptID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return skills.ErrInvalid
		}
		return nil
	}
	history, err := evaluationHistory(ctx, tx, out.TaskID, out.AttemptID)
	if err != nil || len(history) == 0 {
		return skills.ErrInvalid
	}
	record := history[len(history)-1]
	if record.Key != *out.Key {
		return skills.ErrInvalid
	}
	quality, err := evaluation.Resolve(record.Checks, record.AllowJudge)
	if err != nil {
		return err
	}
	if quality.Source == evaluation.Withdrawn {
		return nil
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	out.EvaluationID, out.EvaluationDigest, out.Quality = record.ID, workflowDigest(body), &quality
	return nil
}
