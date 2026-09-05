package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

var ErrWorkflowScanUnavailable = errors.New("workflow scan storage unavailable")

func workflowScanError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrConflict) || errors.Is(err, skills.ErrInvalid) {
		return err
	}
	return ErrWorkflowScanUnavailable
}

const workflowScanProjection = `CASE WHEN length(CAST(scope AS BLOB))<=64 THEN scope END,CASE WHEN length(CAST(name AS BLOB))<=64 THEN name END,revision,length(CAST(body AS BLOB)),CASE WHEN length(CAST(body AS BLOB))<=65536 THEN body END`

func scanWorkflowRecord(row skillGenerationScanner) (scope, name string, revision int64, body []byte, err error) {
	var a, b sql.NullString
	var n sql.NullInt64
	err = row.Scan(&a, &b, &revision, &n, &body)
	if err != nil {
		return
	}
	if !a.Valid || !b.Valid || !workflowSourceID.MatchString(a.String) || !workflowSourceID.MatchString(b.String) || revision < 1 || revision > 1e9 || !n.Valid || n.Int64 < 1 || n.Int64 > 65536 || int64(len(body)) != n.Int64 {
		err = skills.ErrInvalid
		return
	}
	return a.String, b.String, revision, body, nil
}

func readWorkflowScan(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scope, name string) (skills.WorkflowScan, error) {
	var domain sql.NullString
	// Domain is an indexed binding too; bound it independently from JSON.
	err := query.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(domain AS BLOB))<=64 THEN domain END FROM workflow_scans WHERE scope=? AND name=?`, scope, name).Scan(&domain)
	if err != nil {
		return skills.WorkflowScan{}, err
	}
	a, b, rev, body, err := scanWorkflowRecord(query.QueryRowContext(ctx, `SELECT `+workflowScanProjection+` FROM workflow_scans WHERE scope=? AND name=?`, scope, name))
	if err != nil {
		return skills.WorkflowScan{}, err
	}
	var scan skills.WorkflowScan
	if !domain.Valid || json.Unmarshal(body, &scan) != nil || scan.Validate() != nil || scan.Scope != a || scan.Name != b || scan.Revision != rev || scan.Domain != domain.String || a != scope || b != name {
		return skills.WorkflowScan{}, skills.ErrInvalid
	}
	return scan, nil
}

func readWorkflowScanPage(ctx context.Context, tx *sql.Tx, scope, name string, revision int64) (skills.WorkflowScanPage, error) {
	a, b, rev, body, err := scanWorkflowRecord(tx.QueryRowContext(ctx, `SELECT `+workflowScanProjection+` FROM workflow_scan_pages WHERE scope=? AND name=? AND revision=?`, scope, name, revision))
	if err != nil {
		return skills.WorkflowScanPage{}, err
	}
	var page skills.WorkflowScanPage
	if json.Unmarshal(body, &page) != nil || page.Validate() != nil || page.Scan.Scope != a || page.Scan.Name != b || page.Scan.Revision != rev || a != scope || b != name || rev != revision {
		return skills.WorkflowScanPage{}, skills.ErrInvalid
	}
	return page, nil
}

// Unique integer revisions, all positive and with count=max, establish a
// contiguous ledger. SQLite INTEGER affinity alone does not exclude REAL keys.
func workflowScanLedger(ctx context.Context, tx *sql.Tx, scope, name string, expected int64) error {
	var count, minimum, maximum, invalid int64
	err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(min(CASE WHEN typeof(revision)='integer' THEN revision END),0),coalesce(max(CASE WHEN typeof(revision)='integer' THEN revision END),0),coalesce(sum(CASE WHEN typeof(revision)!='integer' THEN 1 ELSE 0 END),0) FROM workflow_scan_pages WHERE scope=? AND name=?`, scope, name).Scan(&count, &minimum, &maximum, &invalid)
	if err != nil {
		return err
	}
	if invalid != 0 || count != expected || maximum != expected || (expected > 0 && minimum != 1) {
		return skills.ErrInvalid
	}
	return nil
}

// WorkflowScan reads durable progress without creating, repairing or advancing
// it. This primitive provides no scheduler or authorization to generate skills.
func (s *Store) WorkflowScan(ctx context.Context, scope, name string) (skills.WorkflowScan, error) {
	if ctx == nil || s == nil || s.db == nil || !workflowSourceID.MatchString(scope) || !workflowSourceID.MatchString(name) {
		return skills.WorkflowScan{}, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return skills.WorkflowScan{}, workflowScanError(ctx, err)
	}
	defer tx.Rollback()
	scan, err := readWorkflowScan(ctx, tx, scope, name)
	if err != nil {
		return skills.WorkflowScan{}, workflowScanError(ctx, err)
	}
	latest, err := readWorkflowScanPage(ctx, tx, scope, name, scan.Revision)
	if err != nil || latest.Scan != scan {
		return skills.WorkflowScan{}, workflowScanError(ctx, skills.ErrInvalid)
	}
	if err = workflowScanLedger(ctx, tx, scope, name, scan.Revision); err != nil {
		return skills.WorkflowScan{}, workflowScanError(ctx, err)
	}
	if err = tx.Commit(); err != nil {
		return skills.WorkflowScan{}, workflowScanError(ctx, err)
	}
	return scan, nil
}

// AdvanceWorkflowScan atomically saves one bounded observation and its cursor.
// Exact revision retries return the historical saved page, even after later
// advances. Completed epochs restart at the beginning with a new upper bound,
// revisiting late feedback and lower-ID arrivals. No inference is dispatched.
func (s *Store) AdvanceWorkflowScan(ctx context.Context, scope, name, domain string, expectedRevision int64, scanLimit int) (page skills.WorkflowScanPage, err error) {
	return s.advanceWorkflowScan(ctx, scope, name, domain, expectedRevision, scanLimit, nil)
}

func (s *Store) advanceWorkflowScan(ctx context.Context, scope, name, domain string, expectedRevision int64, scanLimit int, guard func(context.Context, skills.WorkflowScanPage) error) (page skills.WorkflowScanPage, err error) {
	if ctx == nil || s == nil || s.db == nil || !workflowSourceID.MatchString(scope) || !workflowSourceID.MatchString(name) || !workflowSourceID.MatchString(domain) || expectedRevision < 0 || expectedRevision >= 1e9 || scanLimit < 1 || scanLimit > 20 {
		return page, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			page = skills.WorkflowScanPage{}
			err = workflowScanError(ctx, err)
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_scans SET body=body WHERE scope=? AND name=?`, scope, name); err != nil {
		return page, err
	}
	prior, err := readWorkflowScanPage(ctx, tx, scope, name, expectedRevision+1)
	priorErr := err
	if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return page, priorErr
	}
	head, err := readWorkflowScan(ctx, tx, scope, name)
	if errors.Is(err, sql.ErrNoRows) {
		if priorErr == nil {
			return page, skills.ErrInvalid
		}
		if e := workflowScanLedger(ctx, tx, scope, name, 0); e != nil {
			return page, e
		}
		if expectedRevision != 0 {
			return page, ErrConflict
		}
		head = skills.WorkflowScan{Version: 1, Scope: scope, Name: name, Domain: domain}
	} else if err != nil {
		return page, err
	} else {
		if e := workflowScanLedger(ctx, tx, scope, name, head.Revision); e != nil {
			return page, e
		}
		latest, e := readWorkflowScanPage(ctx, tx, scope, name, head.Revision)
		if e != nil || latest.Scan != head {
			return page, skills.ErrInvalid
		}
		if priorErr == nil {
			if prior.Scan.Domain != head.Domain {
				return page, skills.ErrInvalid
			}
			if head.Revision < prior.Scan.Revision {
				return page, skills.ErrInvalid
			}
			if prior.Limit != scanLimit || prior.Scan.Domain != domain {
				return page, ErrConflict
			}
			if err = guardWorkflowScan(ctx, guard, prior); err != nil {
				return page, err
			}
			if err = tx.Commit(); err != nil {
				return page, err
			}
			return prior, nil
		}
		if head.Revision != expectedRevision || head.Domain != domain {
			return page, ErrConflict
		}
	}
	if head.Revision == 0 || head.Complete {
		head.Epoch++
		head.Cursor = ""
		var upper sql.NullString
		var size sql.NullInt64
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM workflow_scan_tasks`).Scan(&head.Fence); err != nil {
			return page, err
		}
		err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(max(task_id) AS BLOB))<=128 THEN max(task_id) END,length(CAST(max(task_id) AS BLOB)) FROM workflow_scan_tasks WHERE seq<=?`, head.Fence).Scan(&upper, &size)
		if err != nil {
			return page, err
		}
		if size.Valid && (!upper.Valid || !sessions.ValidEventPageID(upper.String)) {
			return page, skills.ErrInvalid
		}
		head.Upper = upper.String
	}
	var heads, mappings, joined int64
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM task_heads),(SELECT count(*) FROM workflow_scan_tasks),(SELECT count(*) FROM workflow_scan_tasks m JOIN task_heads h ON m.task_id=h.task_id WHERE m.seq>0)`).Scan(&heads, &mappings, &joined); err != nil {
		return page, err
	}
	if heads != mappings || heads != joined {
		return page, skills.ErrInvalid
	}
	after := head.Cursor
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(h.task_id AS BLOB)) BETWEEN 1 AND 128 THEN h.task_id END FROM task_heads h JOIN workflow_scan_tasks m ON m.task_id=h.task_id WHERE m.seq<=? AND h.task_id>? AND h.task_id<=? ORDER BY h.task_id LIMIT ?`, head.Fence, after, head.Upper, scanLimit)
	if err != nil {
		return page, err
	}
	ids := []string{}
	previous := after
	for rows.Next() {
		var id sql.NullString
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return page, err
		}
		if !id.Valid || !sessions.ValidEventPageID(id.String) || id.String <= previous {
			rows.Close()
			return page, skills.ErrInvalid
		}
		ids = append(ids, id.String)
		previous = id.String
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	observation := skills.WorkflowCandidatePage{Version: 1, Domain: domain, Scanned: len(ids), Candidates: []skills.WorkflowCandidate{}}
	if len(ids) == scanLimit {
		observation.Next = previous
	}
	for _, id := range ids {
		budget := 256 << 10
		source, e := workflowSource(ctx, tx, id, &budget)
		if errors.Is(e, errWorkflowIneligible) {
			continue
		}
		if e != nil {
			return page, e
		}
		if source.Example.Domain != domain {
			continue
		}
		observation.Candidates = append(observation.Candidates, skills.WorkflowCandidate{TaskID: source.Example.TaskID, SessionID: source.Example.SessionID, Domain: source.Example.Domain, Privacy: source.Privacy, EvaluationID: source.EvaluationID, EvaluationDigest: source.EvaluationDigest, SourceDigest: source.SourceDigest, SourceSequence: source.SourceSequence})
	}
	head.Revision = expectedRevision + 1
	head.Cursor = previous
	head.Complete = len(ids) < scanLimit || head.Cursor == head.Upper
	page = skills.WorkflowScanPage{Version: 1, Scan: head, After: after, Limit: scanLimit, Page: observation}
	if page.Validate() != nil {
		return page, skills.ErrInvalid
	}
	if err = guardWorkflowScan(ctx, guard, page); err != nil {
		return page, err
	}
	pageBody, e := json.Marshal(page)
	if e != nil || len(pageBody) > 65536 {
		return page, skills.ErrInvalid
	}
	headBody, e := json.Marshal(head)
	if e != nil || len(headBody) > 65536 {
		return page, skills.ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workflow_scan_pages(scope,name,revision,body) VALUES(?,?,?,?)`, scope, name, head.Revision, pageBody); err != nil {
		return page, err
	}
	if expectedRevision == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_scans(scope,name,domain,revision,body) VALUES(?,?,?,?,?)`, scope, name, domain, head.Revision, headBody)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE workflow_scans SET revision=?,body=? WHERE scope=? AND name=? AND revision=?`, head.Revision, headBody, scope, name, expectedRevision)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count != 1 {
				err = ErrConflict
			}
		}
	}
	if err != nil {
		return page, err
	}
	if err = tx.Commit(); err != nil {
		return page, err
	}
	return page, nil
}
