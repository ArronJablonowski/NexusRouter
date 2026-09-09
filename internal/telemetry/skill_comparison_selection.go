package telemetry

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type indexedSkillExposure struct {
	task, digest string
	ordinal      int64
}

// SkillComparisonSelection chooses outcome-blind insertion-order windows and
// evaluates them in one snapshot. The observations are returned only for the
// configured application's complete-evidence privacy checks, not HTTP export.
func (s *Store) SkillComparisonSelection(ctx context.Context, policy skills.ComparisonSelectionPolicy) (report skills.ComparisonSelectionReport, observations []skills.TaskOutcome, err error) {
	if s == nil || s.db == nil || ctx == nil || policy.Validate() != nil {
		return report, nil, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			report, observations = skills.ComparisonSelectionReport{}, nil
		}
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, nil, err
	}
	defer tx.Rollback()
	var schema int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || (schema < 27 || schema > 33) {
		return report, nil, skills.ErrInvalid
	}
	report.Version, report.Policy = 1, policy
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM workflow_scan_tasks").Scan(&report.Watermark); err != nil {
		return report, nil, err
	}
	baseline, window, err := selectSkillWindow(ctx, tx, policy, policy.Comparison.BaselineVersion, report.Watermark)
	if err != nil {
		return report, nil, err
	}
	report.Baseline = window
	candidate, window, err := selectSkillWindow(ctx, tx, policy, policy.Comparison.CandidateVersion, report.Watermark)
	if err != nil {
		return report, nil, err
	}
	report.Candidate = window
	selected := append(baseline, candidate...)
	report.Sources = &skills.ComparisonSources{Version: 1, Tasks: make([]string, 0, len(selected))}
	for _, exposure := range selected {
		report.Sources.Tasks = append(report.Sources.Tasks, exposure.task)
	}
	slices.Sort(report.Sources.Tasks)
	observations = make([]skills.TaskOutcome, 0, len(selected))
	if len(selected) != 0 {
		ids := make([]string, 0, len(selected))
		seen := map[string]bool{}
		for _, exposure := range selected {
			if seen[exposure.task] {
				return report, nil, skills.ErrInvalid
			}
			seen[exposure.task] = true
			ids = append(ids, exposure.task)
		}
		if err = preflightSkillTaskOutcomes(ctx, tx, ids); err != nil {
			return report, nil, err
		}
		correlated, correlatedSeen := []string{}, map[string]bool{}
		for i, exposure := range selected {
			outcome, readErr := skillTaskOutcome(ctx, tx, exposure.task)
			if readErr != nil {
				return report, nil, readErr
			}
			version := policy.Comparison.BaselineVersion
			if i >= len(baseline) {
				version = policy.Comparison.CandidateVersion
			}
			if outcome.SkillContext == nil || !outcome.SkillContext.Complete || outcome.Privacy != policy.Privacy {
				return report, nil, skills.ErrInvalid
			}
			matched := false
			for _, ref := range outcome.SkillContext.References {
				if ref.Scope == policy.Comparison.Key.Scope && ref.Name == policy.Comparison.Key.Name && ref.Version == version && ref.Digest == exposure.digest {
					matched = true
				}
			}
			if !matched {
				return report, nil, skills.ErrInvalid
			}
			var ordinal int64
			if err = tx.QueryRowContext(ctx, "SELECT seq FROM workflow_scan_tasks WHERE task_id=?", exposure.task).Scan(&ordinal); err != nil || ordinal != exposure.ordinal {
				return report, nil, skills.ErrInvalid
			}
			var sibling bool
			if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM task_heads WHERE session_id=? AND task_id<>?)", outcome.SessionID, outcome.TaskID).Scan(&sibling); err != nil {
				return report, nil, err
			}
			if sibling && !correlatedSeen[outcome.SessionID] {
				correlated = append(correlated, outcome.SessionID)
				correlatedSeen[outcome.SessionID] = true
			}
			observations = append(observations, outcome)
		}
		body, marshalErr := json.Marshal(observations)
		if marshalErr != nil || len(body) > 4<<20 {
			return report, nil, skills.ErrInvalid
		}
		comparison, compareErr := skills.CompareTaskOutcomesWithCorrelatedSessions(observations, policy.Comparison, correlated)
		if compareErr != nil {
			return report, nil, compareErr
		}
		report.Comparison = &comparison
	}
	if report.Validate() != nil {
		return report, nil, skills.ErrInvalid
	}
	if err = tx.Commit(); err != nil {
		return report, nil, err
	}
	return report, observations, nil
}

func selectSkillWindow(ctx context.Context, tx *sql.Tx, policy skills.ComparisonSelectionPolicy, version string, watermark int64) ([]indexedSkillExposure, skills.ComparisonWindow, error) {
	window := skills.ComparisonWindow{}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END,
	 CASE WHEN length(CAST(digest AS BLOB))=64 THEN digest END, ordinal FROM skill_exposures
	 WHERE scope=? AND name=? AND version=? AND privacy=? AND ordinal<=?
	 ORDER BY ordinal DESC,task_id LIMIT ?`, policy.Comparison.Key.Scope, policy.Comparison.Key.Name, version, policy.Privacy, watermark, policy.TasksPerVersion+1)
	if err != nil {
		return nil, window, err
	}
	defer rows.Close()
	selected := make([]indexedSkillExposure, 0, policy.TasksPerVersion)
	prior := int64(0)
	for rows.Next() {
		var task, digest sql.NullString
		var ordinal int64
		if err = rows.Scan(&task, &digest, &ordinal); err != nil {
			return nil, window, err
		}
		if !task.Valid || !digest.Valid || !sessions.ValidEventPageID(task.String) || !skillExposureDigestValid(digest.String) || ordinal < 1 || ordinal > watermark || prior != 0 && ordinal >= prior {
			return nil, window, skills.ErrInvalid
		}
		prior = ordinal
		if len(selected) == policy.TasksPerVersion {
			window.HasMore = true
			continue
		}
		selected = append(selected, indexedSkillExposure{task.String, digest.String, ordinal})
	}
	if err = rows.Err(); err != nil {
		return nil, window, err
	}
	window.Selected = len(selected)
	if len(selected) > 0 {
		window.NewestOrdinal, window.OldestOrdinal = selected[0].ordinal, selected[len(selected)-1].ordinal
	}
	return selected, window, nil
}

func skillExposureDigestValid(s string) bool {
	decoded, err := hex.DecodeString(s)
	return len(s) == 64 && err == nil && hex.EncodeToString(decoded) == s
}
