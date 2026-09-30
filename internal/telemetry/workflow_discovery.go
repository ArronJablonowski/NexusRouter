package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// DiscoverSkillWorkflows inspects a bounded live page of task heads. The cursor
// advances over ineligible work too; candidates are metadata observations, not
// permission to generate, proof of current freshness, or a frozen search index.
func (s *Store) DiscoverSkillWorkflows(ctx context.Context, domain, after string, scanLimit int) (page skills.WorkflowCandidatePage, err error) {
	if ctx == nil || s == nil || s.db == nil || !workflowSourceID.MatchString(domain) || (after != "" && !sessions.ValidEventPageID(after)) || scanLimit < 1 || scanLimit > 20 {
		return page, errWorkflowSources
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			page, err = skills.WorkflowCandidatePage{}, ctx.Err()
		} else if err != nil {
			page, err = skills.WorkflowCandidatePage{}, errWorkflowSources
		}
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END FROM task_heads WHERE task_id>? ORDER BY task_id LIMIT ?`, after, scanLimit)
	if err != nil {
		return page, err
	}
	ids := make([]string, 0, scanLimit)
	previous := after
	for rows.Next() {
		var id sql.NullString
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return page, err
		}
		if !id.Valid || !sessions.ValidEventPageID(id.String) || id.String <= previous {
			rows.Close()
			return page, errWorkflowSources
		}
		ids = append(ids, id.String)
		previous = id.String
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	page = skills.WorkflowCandidatePage{Version: 1, Domain: domain, Candidates: make([]skills.WorkflowCandidate, 0, len(ids)), Scanned: len(ids)}
	if len(ids) == scanLimit {
		page.Next = ids[len(ids)-1]
	}
	for _, id := range ids {
		budget := 256 << 10
		source, sourceErr := workflowSource(ctx, tx, id, &budget)
		if errors.Is(sourceErr, errWorkflowIneligible) {
			continue
		}
		if sourceErr != nil {
			return page, sourceErr
		}
		if source.Example.Domain != domain {
			continue
		}
		page.Candidates = append(page.Candidates, skills.WorkflowCandidate{TaskID: source.Example.TaskID, SessionID: source.Example.SessionID, Domain: source.Example.Domain, Privacy: source.Privacy, EvaluationID: source.EvaluationID, EvaluationDigest: source.EvaluationDigest, SourceDigest: source.SourceDigest, SourceSequence: source.SourceSequence})
	}
	if page.Validate(after, scanLimit) != nil {
		return page, errWorkflowSources
	}
	if err = tx.Commit(); err != nil {
		return page, err
	}
	return page, nil
}
