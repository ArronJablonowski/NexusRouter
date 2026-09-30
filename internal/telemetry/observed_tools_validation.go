package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

var errObservedToolsValidation = errors.New("observed tools validation unavailable")

// ErrObservedToolsEvidence means the coherent snapshot was readable but the
// current source evidence no longer proves the saved observed-tools selection.
// Callers may treat this as a deterministic failed provenance check. Storage,
// cancellation, corruption, and concurrency failures use a different error.
var ErrObservedToolsEvidence = errors.New("observed tools evidence rejected")

// ObservedToolsValidationSnapshot is a value-only view of one published
// generation's durable telemetry provenance. All records and source
// observations are read and re-derived in one SQLite transaction.
type ObservedToolsValidationSnapshot struct {
	Attempt   skills.GenerationAttempt
	Selection skills.WorkflowSelection
	Group     skills.WorkflowGroup
	LocalOnly bool
}

// ObservedToolsValidationSnapshot re-derives the exact observed-tools group
// and current accepted source records bound by one generation attempt. It does
// not execute a provider, tool, candidate validation case, or storage write.
func (s *Store) ObservedToolsValidationSnapshot(ctx context.Context, scope, attemptID string) (out ObservedToolsValidationSnapshot, err error) {
	return s.observedToolsValidationSnapshot(ctx, scope, attemptID, nil)
}

func (s *Store) observedToolsValidationSnapshot(ctx context.Context, scope, attemptID string, afterSnapshot func()) (out ObservedToolsValidationSnapshot, err error) {
	if ctx == nil || s == nil || s.db == nil || !workflowSourceID.MatchString(scope) || !skillGenerationID(attemptID) {
		return out, skills.ErrInvalid
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if bounded.Err() != nil {
			out, err = ObservedToolsValidationSnapshot{}, bounded.Err()
		} else if errors.Is(err, ErrObservedToolsEvidence) {
			out, err = ObservedToolsValidationSnapshot{}, ErrObservedToolsEvidence
		} else if err != nil {
			out, err = ObservedToolsValidationSnapshot{}, errObservedToolsValidation
		}
	}()
	var dataVersionBefore int64
	if err = s.db.QueryRowContext(bounded, `PRAGMA data_version`).Scan(&dataVersionBefore); err != nil || dataVersionBefore < 1 {
		return out, errObservedToolsValidation
	}

	tx, err := s.db.BeginTx(bounded, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	attempt, err := readSkillGeneration(bounded, tx, attemptID)
	if err != nil || attempt.Status != "drafted" || attempt.Result == nil || attempt.ID != attemptID || attempt.Key.Scope != scope {
		return out, errObservedToolsValidation
	}
	selection, err := decodeWorkflowSelection(tx.QueryRowContext(bounded, `SELECT `+workflowSelectionColumns+` FROM workflow_selections WHERE scope=? AND id=?`, scope, attemptID))
	if err != nil || selection.ID != attempt.ID || selection.Key != attempt.Key || selection.Algorithm != skills.ObservedToolsAlgorithm {
		return out, errObservedToolsValidation
	}

	procedures := make([]skills.WorkflowProcedure, 0, len(selection.Sources))
	current := make([]skills.WorkflowCandidate, 0, len(selection.Sources))
	budget := 256 << 10
	localOnly := false
	for _, selected := range selection.Sources {
		source, readErr := workflowSource(bounded, tx, selected.TaskID, &budget)
		if errors.Is(readErr, errWorkflowIneligible) {
			return out, ErrObservedToolsEvidence
		}
		if readErr != nil || len(source.Example.Checks) != 1 {
			return out, errObservedToolsValidation
		}
		// Judge-only evidence is never sufficient for automatic provenance
		// validation. Resolve(false), used by workflowSource, also rejects it;
		// retain this explicit fence against future selector changes.
		if source.Example.Checks[0].Source == evaluation.LLMJudge {
			return out, ErrObservedToolsEvidence
		}
		procedure, readErr := workflowProcedure(bounded, tx, source)
		if errors.Is(readErr, errWorkflowIneligible) {
			return out, ErrObservedToolsEvidence
		}
		if readErr != nil {
			return out, errObservedToolsValidation
		}
		procedures = append(procedures, procedure)
		current = append(current, procedure.Candidate)
		localOnly = localOnly || source.Privacy != "cloud_allowed"
	}
	if !slices.Equal(current, selection.Sources) {
		return out, ErrObservedToolsEvidence
	}
	groups, err := skills.BuildWorkflowGroups(procedures)
	if err != nil || len(groups) != 1 || groups[0].ID != selection.Group || groups[0].Algorithm != selection.Algorithm || !slices.Equal(groups[0].Sources, selection.Sources) {
		return out, ErrObservedToolsEvidence
	}

	if err = tx.Commit(); err != nil {
		return out, err
	}
	if afterSnapshot != nil {
		afterSnapshot()
	}
	var dataVersionAfter int64
	if err = s.db.QueryRowContext(bounded, `PRAGMA data_version`).Scan(&dataVersionAfter); err != nil || dataVersionAfter != dataVersionBefore {
		return out, errObservedToolsValidation
	}
	return ObservedToolsValidationSnapshot{Attempt: attempt, Selection: selection, Group: groups[0], LocalOnly: localOnly}, nil
}
