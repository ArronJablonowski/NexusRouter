package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// SkillTaskOutcomes observes an explicit set in one SQLite snapshot. Input order
// is not changed; output is sorted by task ID. No partial result is returned.
// All journal and evidence body lengths are preflighted before loading bodies.
func (s *Store) SkillTaskOutcomes(ctx context.Context, tasks []string) (out []skills.TaskOutcome, err error) {
	if ctx == nil || s == nil || s.db == nil || len(tasks) < 1 || len(tasks) > 200 {
		return nil, skills.ErrInvalid
	}
	ids := append([]string(nil), tasks...)
	slices.Sort(ids)
	for i, id := range ids {
		if !sessions.ValidEventPageID(id) || i > 0 && ids[i-1] == id {
			return nil, skills.ErrInvalid
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			out, err = nil, ctx.Err()
		} else if err != nil {
			out = nil
		}
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = preflightSkillTaskOutcomes(ctx, tx, ids); err != nil {
		return nil, err
	}
	out = make([]skills.TaskOutcome, 0, len(ids))
	for _, id := range ids {
		item, e := skillTaskOutcome(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, item)
	}
	body, err := json.Marshal(out)
	if err != nil || len(body) > 4<<20 {
		return nil, skills.ErrInvalid
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func preflightSkillTaskOutcomes(ctx context.Context, tx *sql.Tx, ids []string) error {
	eventCount, eventBytes, evidenceCount, evidenceBytes := int64(0), int64(0), int64(0), int64(0)
	for _, id := range ids {
		var head int64
		if err := tx.QueryRowContext(ctx, "SELECT sequence FROM task_heads WHERE task_id=?", id).Scan(&head); err != nil {
			return err
		}
		if head < 1 || head > 10000-eventCount {
			return skills.ErrInvalid
		}
		count, size, err := skillOutcomeSizes(ctx, tx, `SELECT length(CAST(body AS BLOB)) FROM events WHERE task_id=? ORDER BY sequence LIMIT 10001`, id)
		if err != nil {
			return err
		}
		if count != head || count > 10000-eventCount || size > 8<<20-eventBytes {
			return skills.ErrInvalid
		}
		eventCount += count
		eventBytes += size
		// Bound evidence metadata rows too: at most 10,000 base/revision rows
		// across the explicit set, including nonfinal attempts. Their bodies are
		// not read here; final-attempt per-record/chain guards still run below.
		for _, query := range []string{
			`SELECT length(CAST(body AS BLOB)) FROM evaluations WHERE task_id=? LIMIT 10001`,
			`SELECT length(CAST(r.body AS BLOB)) FROM evaluation_revisions r JOIN evaluations e ON e.id=r.base_id WHERE e.task_id=? LIMIT 10001`,
		} {
			count, size, err = skillOutcomeSizes(ctx, tx, query, id)
			if err != nil {
				return err
			}
			if count > 10000-evidenceCount || size > 8<<20-evidenceBytes {
				return skills.ErrInvalid
			}
			evidenceCount += count
			evidenceBytes += size
		}
	}
	return nil
}

func skillOutcomeSizes(ctx context.Context, tx *sql.Tx, query, id string) (count, size int64, err error) {
	rows, err := tx.QueryContext(ctx, query, id)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			return 0, 0, err
		}
		if n < 1 || n > 8<<20-size || count >= 10000 {
			return 0, 0, skills.ErrInvalid
		}
		size += n
		count++
	}
	return count, size, rows.Err()
}
