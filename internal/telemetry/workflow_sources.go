package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

var errWorkflowSources = errors.New("skill workflow sources unavailable")
var workflowSourceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// SkillWorkflowSources reads completed conversations and their current accepted
// final-attempt evaluations in one coherent read transaction. Steps contain
// observed JSON messages, not executable instructions or model-rated success.
// Results are sensitive: the host must redact and enforce privacy before sending
// them to any generator. This operation never repairs, generates or mutates data.
func (s *Store) SkillWorkflowSources(ctx context.Context, tasks []string) (sources []skills.WorkflowSource, err error) {
	if ctx == nil || s == nil || s.db == nil || len(tasks) < 2 || len(tasks) > 20 {
		return nil, errWorkflowSources
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if bounded.Err() != nil {
			sources, err = nil, bounded.Err()
		} else if err != nil {
			sources, err = nil, errWorkflowSources
		}
	}()
	seenTasks, seenSessions := map[string]bool{}, map[string]bool{}
	for _, task := range tasks {
		if !workflowSourceID.MatchString(task) || seenTasks[task] {
			return nil, errWorkflowSources
		}
		seenTasks[task] = true
	}
	tx, err := s.db.BeginTx(bounded, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	examples := make([]skills.WorkflowExample, 0, len(tasks))
	budget := 256 << 10
	domain := ""
	for _, task := range tasks {
		var events []runtime.Event
		snapshot, err := taskSnapshotWithEvents(bounded, tx, task, &events)
		if err != nil {
			return nil, err
		}
		if snapshot.State != "completed" || snapshot.InterruptedTurn || snapshot.UncertainEffects || len(snapshot.Pending) != 0 || !workflowSourceID.MatchString(snapshot.SessionID) || seenSessions[snapshot.SessionID] || (snapshot.Privacy != "local_only" && snapshot.Privacy != "cloud_allowed") {
			return nil, errWorkflowSources
		}
		seenSessions[snapshot.SessionID] = true
		key, attempt, err := workflowFinalAttempt(events)
		if err != nil || (domain != "" && key.Domain != domain) {
			return nil, errWorkflowSources
		}
		domain = key.Domain
		if err = preflightWorkflowEvaluation(bounded, tx, task, attempt, key); err != nil {
			return nil, err
		}
		history, err := evaluationHistory(bounded, tx, task, attempt)
		if err != nil || len(history) == 0 {
			return nil, errWorkflowSources
		}
		record := history[len(history)-1]
		outcome, err := evaluation.Resolve(record.Checks, false)
		if err != nil || !outcome.Accepted || !record.ExecutionSucceeded || record.Key != key || record.TaskID != task || record.AttemptID != attempt {
			return nil, errWorkflowSources
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		evaluationDigest := workflowDigest(encoded)
		example := skills.WorkflowExample{SessionID: snapshot.SessionID, TaskID: task, Domain: key.Domain, Checks: []evaluation.Check{{Source: outcome.Source, Reference: evaluationDigest, Passed: true}}}
		for _, message := range snapshot.Messages {
			if message.Role == "system" {
				continue
			}
			if len(example.Steps) >= 128 {
				return nil, errWorkflowSources
			}
			body, err := json.Marshal(message)
			if err != nil || len(body) > budget {
				return nil, errWorkflowSources
			}
			budget -= len(body)
			example.Steps = append(example.Steps, string(body))
		}
		encoded, err = json.Marshal(snapshot)
		if err != nil {
			return nil, err
		}
		examples = append(examples, example)
		sources = append(sources, skills.WorkflowSource{Example: example, Privacy: snapshot.Privacy, EvaluationID: record.ID, EvaluationDigest: evaluationDigest, SourceDigest: workflowDigest(encoded), SourceSequence: snapshot.Sequence})
	}
	if skills.ValidateWorkflowExamples(skills.Key{Scope: "workflow", Name: "sources"}, examples) != nil {
		return nil, errWorkflowSources
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return sources, nil
}

func workflowFinalAttempt(events []runtime.Event) (routing.Key, string, error) {
	var key routing.Key
	var domain, profile string
	var started, completed runtime.Event
	for _, event := range events {
		switch event.Kind {
		case runtime.TaskStarted:
			domain, profile = event.Data.Domain, event.Data.Profile
			// Match the application feedback contract for omitted explicit-route
			// labels; these defaults are not inferred from model output.
			if domain == "" {
				domain = "general"
			}
			if profile == "" {
				profile = "default"
			}
		case runtime.RouteSelected:
			domain, profile = event.Data.Domain, event.Data.Profile
		case runtime.TurnStarted:
			started, completed = event, runtime.Event{}
			key = routing.Key{Model: event.Data.ModelID, Provider: event.Data.ProviderID, Domain: domain, Profile: profile}
		case runtime.TurnCompleted:
			completed = event
		}
	}
	if !workflowSourceID.MatchString(key.Domain) || !workflowSourceID.MatchString(key.Profile) || !workflowModelID(key.Model) || !workflowModelID(key.Provider) || !sessions.ValidEventPageID(started.AttemptID) || completed.AttemptID != started.AttemptID || completed.TurnID != started.TurnID || completed.Sequence <= started.Sequence || completed.Time.Before(started.Time) {
		return routing.Key{}, "", errWorkflowSources
	}
	return key, started.AttemptID, nil
}

func workflowModelID(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func preflightWorkflowEvaluation(ctx context.Context, tx *sql.Tx, task, attempt string, key routing.Key) error {
	var base, head, model, provider, domain, profile sql.NullString
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT
	CASE WHEN length(CAST(e.id AS BLOB)) BETWEEN 1 AND 128 THEN e.id END,
	CASE WHEN length(CAST(h.current_id AS BLOB)) BETWEEN 1 AND 128 THEN h.current_id END,
	CASE WHEN length(CAST(e.model AS BLOB)) BETWEEN 1 AND 128 THEN e.model END,
	CASE WHEN length(CAST(e.provider AS BLOB)) BETWEEN 1 AND 128 THEN e.provider END,
	CASE WHEN length(CAST(e.domain AS BLOB)) BETWEEN 1 AND 128 THEN e.domain END,
	CASE WHEN length(CAST(e.profile AS BLOB)) BETWEEN 1 AND 128 THEN e.profile END,
	length(CAST(e.body AS BLOB)) FROM evaluations e JOIN evaluation_heads h ON h.base_id=e.id WHERE e.task_id=? AND e.attempt_id=?`, task, attempt).Scan(&base, &head, &model, &provider, &domain, &profile, &size)
	if err != nil {
		return err
	}
	if !base.Valid || !head.Valid || !sessions.ValidEventPageID(base.String) || !sessions.ValidEventPageID(head.String) || !model.Valid || !provider.Valid || !domain.Valid || !profile.Valid || model.String != key.Model || provider.String != key.Provider || domain.String != key.Domain || profile.String != key.Profile || size < 1 || size > 256<<10 {
		return errWorkflowSources
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id END,CASE WHEN length(CAST(supersedes AS BLOB)) BETWEEN 1 AND 128 THEN supersedes END,length(CAST(body AS BLOB)) FROM evaluation_revisions WHERE base_id=? ORDER BY rowid LIMIT 102`, base.String)
	if err != nil {
		return err
	}
	defer rows.Close()
	count, total := 0, size
	for rows.Next() {
		var id, previous sql.NullString
		var length int64
		if rows.Scan(&id, &previous, &length) != nil || !id.Valid || !previous.Valid || !sessions.ValidEventPageID(id.String) || !sessions.ValidEventPageID(previous.String) || count >= 100 || length < 1 || length > 256<<10 || length > (1<<20)-total {
			return errWorkflowSources
		}
		count++
		total += length
	}
	return rows.Err()
}

func workflowDigest(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}
