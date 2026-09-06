package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// CheckSkillComparisonSources checks only the saved task IDs in one current
// snapshot. It never chooses replacement tasks or refreshes latest windows.
// Returned observations are for host privacy checks, not public export. A
// successful check does not lock evidence against subsequent writer commits.
func (s *Store) CheckSkillComparisonSources(ctx context.Context, report skills.ComparisonSelectionReport) (out []skills.TaskOutcome, err error) {
	if s == nil || s.db == nil || ctx == nil || report.Validate() != nil || report.Sources == nil {
		return nil, skills.ErrInvalid
	}
	// Own callback-supplied nested data for the duration of database I/O.
	body, e := json.Marshal(report)
	if e != nil || len(body) > 64<<10 {
		return nil, skills.ErrInvalid
	}
	var owned skills.ComparisonSelectionReport
	if e = json.Unmarshal(body, &owned); e != nil {
		return nil, skills.ErrInvalid
	}
	report = owned
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			out = nil
		}
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var schema int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || (schema < 27 || schema > 28) {
		return nil, skills.ErrInvalid
	}
	ids := report.Sources.Tasks
	out = make([]skills.TaskOutcome, 0, len(ids))
	if len(ids) > 0 {
		if err = preflightSkillTaskOutcomes(ctx, tx, ids); err != nil {
			return nil, err
		}
		correlated := []string{}
		seen := map[string]bool{}
		windows := [2]skills.ComparisonWindow{}
		for _, id := range ids {
			o, e := skillTaskOutcome(ctx, tx, id)
			if e != nil {
				return nil, e
			}
			if o.Privacy != report.Policy.Privacy || o.SkillContext == nil || !o.SkillContext.Complete {
				return nil, skills.ErrConflict
			}
			cohort := -1
			for _, ref := range o.SkillContext.References {
				if ref.Scope == report.Policy.Comparison.Key.Scope && ref.Name == report.Policy.Comparison.Key.Name {
					if ref.Version == report.Policy.Comparison.BaselineVersion {
						cohort = 0
					}
					if ref.Version == report.Policy.Comparison.CandidateVersion {
						cohort = 1
					}
				}
			}
			if cohort < 0 {
				return nil, skills.ErrConflict
			}
			var ordinal int64
			if err = tx.QueryRowContext(ctx, "SELECT seq FROM workflow_scan_tasks WHERE task_id=?", id).Scan(&ordinal); err != nil {
				return nil, err
			}
			if ordinal < 1 || ordinal > report.Watermark {
				return nil, skills.ErrConflict
			}
			w := &windows[cohort]
			w.Selected++
			if w.OldestOrdinal == 0 || ordinal < w.OldestOrdinal {
				w.OldestOrdinal = ordinal
			}
			if ordinal > w.NewestOrdinal {
				w.NewestOrdinal = ordinal
			}
			var sibling bool
			if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM task_heads WHERE session_id=? AND task_id<>?)", o.SessionID, o.TaskID).Scan(&sibling); err != nil {
				return nil, err
			}
			if sibling && !seen[o.SessionID] {
				seen[o.SessionID] = true
				correlated = append(correlated, o.SessionID)
			}
			out = append(out, o)
		}
		// HasMore is historical window metadata, deliberately not queried again.
		windows[0].HasMore = report.Baseline.HasMore
		windows[1].HasMore = report.Candidate.HasMore
		if windows[0] != report.Baseline || windows[1] != report.Candidate {
			return nil, skills.ErrConflict
		}
		body, e = json.Marshal(out)
		if e != nil || len(body) > 4<<20 {
			return nil, skills.ErrInvalid
		}
		comparison, e := skills.CompareTaskOutcomesWithCorrelatedSessions(out, report.Policy.Comparison, correlated)
		if e != nil {
			return nil, e
		}
		comparison.ConfiguredModelID = report.ConfiguredModelID
		if !reflect.DeepEqual(comparison, *report.Comparison) {
			return nil, skills.ErrConflict
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
