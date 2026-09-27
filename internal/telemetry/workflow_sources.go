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
var errWorkflowIneligible = errors.New("skill workflow is not eligible")
var workflowSourceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// SkillWorkflowSources reads completed conversations and their current accepted
// final-attempt evaluations in one coherent read transaction. Steps contain
// observed JSON messages, not executable instructions or model-rated success.
// Results are sensitive: the host must redact and enforce privacy before sending
// them to any generator. This operation never repairs, generates or mutates data.
func (s *Store) SkillWorkflowSources(ctx context.Context, tasks []string) (sources []skills.WorkflowSource, err error) {
	return s.skillWorkflowSources(ctx, tasks, "")
}

// SkillWorkflowGroupSources rechecks the observed tool grouping in the same
// transaction as accepted source extraction. Snapshot hashes alone do not bind
// actual tool execution order or failure codes.
func (s *Store) SkillWorkflowGroupSources(ctx context.Context, tasks []string, groupID string) ([]skills.WorkflowSource, error) {
	if !workflowSelectionID(groupID) {
		return nil, errWorkflowSources
	}
	return s.skillWorkflowSources(ctx, tasks, groupID)
}

func (s *Store) skillWorkflowSources(ctx context.Context, tasks []string, groupID string) (sources []skills.WorkflowSource, err error) {
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
	var procedures []skills.WorkflowProcedure
	for _, task := range tasks {
		source, err := workflowSource(bounded, tx, task, &budget)
		if err != nil {
			return nil, err
		}
		if seenSessions[source.Example.SessionID] || (domain != "" && source.Example.Domain != domain) {
			return nil, errWorkflowSources
		}
		seenSessions[source.Example.SessionID] = true
		domain = source.Example.Domain
		examples = append(examples, source.Example)
		sources = append(sources, source)
		if groupID != "" {
			procedure, err := workflowProcedure(bounded, tx, source)
			if err != nil {
				return nil, err
			}
			procedures = append(procedures, procedure)
		}
	}
	if skills.ValidateWorkflowExamples(skills.Key{Scope: "workflow", Name: "sources"}, examples) != nil {
		return nil, errWorkflowSources
	}
	if groupID != "" {
		groups, err := skills.BuildWorkflowGroups(procedures)
		if err != nil || len(groups) != 1 || groups[0].ID != groupID || len(groups[0].Sources) != len(tasks) {
			return nil, errWorkflowSources
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return sources, nil
}

// workflowSource separates valid-but-ineligible work from corrupt persisted
// history. The caller owns the coherent read transaction and source-byte budget.
func workflowSource(ctx context.Context, tx *sql.Tx, task string, budget *int) (skills.WorkflowSource, error) {
	zero := skills.WorkflowSource{}
	var events []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, task, &events)
	if err != nil {
		return zero, err
	}
	if snapshot.State != "completed" || snapshot.InterruptedTurn || snapshot.UncertainEffects || len(snapshot.Pending) != 0 {
		return zero, errWorkflowIneligible
	}
	if !workflowSourceID.MatchString(task) || !workflowSourceID.MatchString(snapshot.SessionID) {
		return zero, errWorkflowIneligible
	}
	if snapshot.Privacy != "local_only" && snapshot.Privacy != "cloud_allowed" {
		return zero, errWorkflowSources
	}
	key, attempt, err := workflowFinalAttempt(events)
	if err != nil {
		return zero, err
	}
	if !workflowSourceID.MatchString(key.Domain) || !workflowSourceID.MatchString(key.Profile) {
		return zero, errWorkflowIneligible
	}
	if err = preflightWorkflowEvaluation(ctx, tx, task, attempt, key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM evaluations WHERE task_id=? AND attempt_id=?`, task, attempt).Scan(&count); err != nil {
				return zero, err
			}
			if count == 0 {
				return zero, errWorkflowIneligible
			}
		}
		return zero, errWorkflowSources
	}
	history, err := evaluationHistory(ctx, tx, task, attempt)
	if err != nil || len(history) == 0 {
		return zero, errWorkflowSources
	}
	record := history[len(history)-1]
	if record.Key != key || record.TaskID != task || record.AttemptID != attempt {
		return zero, errWorkflowSources
	}
	outcome, err := evaluation.Resolve(record.Checks, false)
	if err != nil {
		if _, judgeErr := evaluation.Resolve(record.Checks, true); judgeErr == nil {
			return zero, errWorkflowIneligible
		}
		return zero, errWorkflowSources
	}
	if outcome.Source == evaluation.Withdrawn || !outcome.Accepted || !record.ExecutionSucceeded {
		return zero, errWorkflowIneligible
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return zero, err
	}
	evaluationDigest := workflowDigest(encoded)
	example := skills.WorkflowExample{SessionID: snapshot.SessionID, TaskID: task, Domain: key.Domain, Checks: []evaluation.Check{{Source: outcome.Source, Reference: evaluationDigest, Passed: true}}}
	for _, message := range snapshot.Messages {
		if message.Role == "system" {
			continue
		}
		if len(example.Steps) >= 128 {
			return zero, errWorkflowIneligible
		}
		body, err := json.Marshal(message)
		if err != nil {
			return zero, errWorkflowSources
		}
		if len(body) > *budget {
			return zero, errWorkflowIneligible
		}
		*budget -= len(body)
		example.Steps = append(example.Steps, string(body))
	}
	if len(example.Steps) == 0 {
		return zero, errWorkflowSources
	}
	encoded, err = json.Marshal(snapshot)
	if err != nil {
		return zero, err
	}
	return skills.WorkflowSource{Example: example, Privacy: snapshot.Privacy, EvaluationID: record.ID, EvaluationDigest: evaluationDigest, SourceDigest: workflowDigest(encoded), SourceSequence: snapshot.Sequence}, nil
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
	if !workflowModelID(key.Domain) || !workflowModelID(key.Profile) || !workflowModelID(key.Model) || !workflowModelID(key.Provider) || !sessions.ValidEventPageID(started.AttemptID) || completed.AttemptID != started.AttemptID || completed.TurnID != started.TurnID || completed.Sequence <= started.Sequence || completed.Time.Before(started.Time) {
		return routing.Key{}, "", errWorkflowSources
	}
	return key, started.AttemptID, nil
}

func workflowModelID(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func preflightWorkflowEvaluation(ctx context.Context, tx *sql.Tx, task, attempt string, key routing.Key) error {
	return preflightEvaluationKey(ctx, tx, task, attempt, key, 128)
}

// Key limits belong to the caller's model catalog, not event identity rules.
// Workflow discovery keeps its original bound; deprecation accepts longer tags.
func preflightEvaluationKey(ctx context.Context, tx *sql.Tx, task, attempt string, key routing.Key, keyLimit int) error {
	var base, head, model, provider, domain, profile sql.NullString
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT
	CASE WHEN length(CAST(e.id AS BLOB)) BETWEEN 1 AND 128 THEN e.id END,
	CASE WHEN length(CAST(h.current_id AS BLOB)) BETWEEN 1 AND 128 THEN h.current_id END,
	CASE WHEN length(CAST(e.model AS BLOB)) BETWEEN 1 AND ? THEN e.model END,
	CASE WHEN length(CAST(e.provider AS BLOB)) BETWEEN 1 AND ? THEN e.provider END,
	CASE WHEN length(CAST(e.domain AS BLOB)) BETWEEN 1 AND ? THEN e.domain END,
	CASE WHEN length(CAST(e.profile AS BLOB)) BETWEEN 1 AND ? THEN e.profile END,
	length(CAST(e.body AS BLOB)) FROM evaluations e JOIN evaluation_heads h ON h.base_id=e.id WHERE e.task_id=? AND e.attempt_id=?`, keyLimit, keyLimit, keyLimit, keyLimit, task, attempt).Scan(&base, &head, &model, &provider, &domain, &profile, &size)
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
