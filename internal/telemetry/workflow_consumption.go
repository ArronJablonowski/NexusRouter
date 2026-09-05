package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// ConsumeWorkflowScan consumes exactly one durable page. It refreshes objective
// evidence within the writer transaction, but the receipt remains a historical
// observation, never authority to generate, publish, or execute a skill. The
// mandatory trusted guard runs cooperatively under the transaction lock.
func (s *Store) ConsumeWorkflowScan(ctx context.Context, scope, name string, expectedRevision int64, guard func(context.Context, skills.WorkflowScanConsumption) error) (out skills.WorkflowScanConsumption, err error) {
	if ctx == nil || s == nil || s.db == nil || !workflowSourceID.MatchString(scope) || !workflowSourceID.MatchString(name) || expectedRevision < 0 || expectedRevision >= 1e9 || guard == nil {
		return out, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			out = skills.WorkflowScanConsumption{}
			err = workflowScanError(ctx, err)
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_scan_consumers SET body=body WHERE scope=? AND name=?`, scope, name); err != nil {
		return out, err
	}
	source, err := consumptionSourceHead(ctx, tx, scope, name)
	if err != nil {
		return out, err
	}
	head, err := consumptionHead(ctx, tx, scope, name)
	if errors.Is(err, sql.ErrNoRows) {
		if err = consumptionLedger(ctx, tx, scope, name, 0); err != nil {
			return out, err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_scan_buckets WHERE scope=? AND name=?`, scope, name).Scan(&count); err != nil {
			return out, err
		}
		if count != 0 {
			return out, skills.ErrInvalid
		}
		if expectedRevision != 0 {
			return out, ErrConflict
		}
	} else if err != nil {
		return out, err
	} else {
		if head.Revision > source.Revision || head.Domain != source.Domain {
			return out, skills.ErrInvalid
		}
		if expectedRevision < head.Revision {
			prior, e := readConsumption(ctx, tx, scope, name, expectedRevision+1, false)
			if e != nil {
				return out, e
			}
			if e = consumptionBinding(ctx, tx, prior); e != nil {
				return out, e
			}
			if e = guardConsumption(ctx, guard, prior); e != nil {
				return out, e
			}
			if e = tx.Commit(); e != nil {
				return out, e
			}
			return prior, nil
		}
		if expectedRevision != head.Revision {
			return out, ErrConflict
		}
	}
	page, err := consumptionPage(ctx, tx, scope, name, expectedRevision+1)
	if err != nil {
		return out, err
	}
	if page.Scan.Domain != source.Domain || page.Scan.Revision > source.Revision {
		return out, skills.ErrInvalid
	}
	out = skills.WorkflowScanConsumption{Version: 1, Scope: scope, Name: name, Domain: page.Scan.Domain, Epoch: page.Scan.Epoch, Revision: page.Scan.Revision, PageDigest: consumptionDigest(page), Considered: len(page.Page.Candidates), Buckets: []skills.WorkflowBucket{}}
	count, err := consumptionBucketCount(ctx, tx, scope, name, out.Epoch)
	if err != nil {
		return out, err
	}
	touched := map[string]skills.WorkflowBucket{}
	budget := 256 << 10
	for _, candidate := range page.Page.Candidates {
		fresh, e := workflowSource(ctx, tx, candidate.TaskID, &budget)
		if errors.Is(e, errWorkflowIneligible) {
			continue
		}
		if e != nil {
			return out, e
		}
		p, e := workflowProcedure(ctx, tx, fresh)
		if errors.Is(e, errWorkflowIneligible) {
			continue
		}
		if e != nil {
			return out, e
		}
		if p.Candidate != candidate || len(p.Tools) == 0 {
			continue
		}
		out.Eligible++
		b, e := skills.NewWorkflowBucket(p)
		if e != nil {
			return out, e
		}
		current, ok := touched[b.ID]
		if !ok {
			current, e = readConsumptionBucket(ctx, tx, scope, name, out.Epoch, b.ID)
			if errors.Is(e, sql.ErrNoRows) {
				count++
				if count > 1000 {
					return out, skills.ErrInvalid
				}
				current = b
			} else if e != nil {
				return out, e
			}
		}
		current, e = current.Merge(p)
		if e != nil {
			return out, e
		}
		touched[b.ID] = current
	}
	for _, b := range touched {
		out.Buckets = append(out.Buckets, b)
	}
	slices.SortFunc(out.Buckets, func(a, b skills.WorkflowBucket) int { return strings.Compare(a.ID, b.ID) })
	if err = guardConsumption(ctx, guard, out); err != nil {
		return out, err
	}
	for _, b := range out.Buckets {
		body, e := json.Marshal(b)
		if e != nil || len(body) > 65536 {
			return out, skills.ErrInvalid
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO workflow_scan_buckets(scope,name,epoch,id,revision,body) VALUES(?,?,?,?,?,?) ON CONFLICT(scope,name,epoch,id) DO UPDATE SET revision=excluded.revision,body=excluded.body`, scope, name, out.Epoch, b.ID, out.Revision, body); err != nil {
			return out, err
		}
	}
	body, err := json.Marshal(out)
	if err != nil || len(body) > 524288 {
		return out, skills.ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workflow_scan_consumptions(scope,name,revision,body) VALUES(?,?,?,?)`, scope, name, out.Revision, body); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workflow_scan_consumers(scope,name,revision,body) VALUES(?,?,?,?) ON CONFLICT(scope,name) DO UPDATE SET revision=excluded.revision,body=excluded.body`, scope, name, out.Revision, body); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

// WorkflowScanConsumption inspects the latest receipt without repairing state.
func (s *Store) WorkflowScanConsumption(ctx context.Context, scope, name string) (out skills.WorkflowScanConsumption, err error) {
	if ctx == nil || s == nil || s.db == nil || !workflowSourceID.MatchString(scope) || !workflowSourceID.MatchString(name) {
		return out, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			out = skills.WorkflowScanConsumption{}
			err = workflowScanError(ctx, err)
		}
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	source, err := consumptionSourceHead(ctx, tx, scope, name)
	if err != nil {
		return out, err
	}
	out, err = consumptionHead(ctx, tx, scope, name)
	if err != nil {
		return out, err
	}
	if out.Revision > source.Revision || out.Domain != source.Domain {
		return out, skills.ErrInvalid
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

// ListWorkflowScanBuckets includes singleton observations so pagination never
// skips a future grouping candidate. Callers must revalidate sources before use.
func (s *Store) ListWorkflowScanBuckets(ctx context.Context, scope, name string, epoch int64, afterID string, limit int) (out []skills.WorkflowBucket, err error) {
	if ctx == nil || s == nil || s.db == nil || !workflowSourceID.MatchString(scope) || !workflowSourceID.MatchString(name) || epoch < 1 || epoch > 1e9 || limit < 1 || limit > 20 || (afterID != "" && (len(afterID) != 64 || strings.Trim(afterID, "0123456789abcdef") != "")) {
		return nil, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			out = nil
			err = workflowScanError(ctx, err)
		}
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := consumptionSourceHead(ctx, tx, scope, name)
	if err != nil {
		return nil, err
	}
	head, err := consumptionHead(ctx, tx, scope, name)
	if err != nil {
		return nil, err
	}
	if head.Revision > source.Revision || head.Domain != source.Domain || epoch > head.Epoch {
		return nil, skills.ErrInvalid
	}
	if _, err = consumptionBucketCount(ctx, tx, scope, name, epoch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workflow_scan_buckets WHERE scope=? AND name=? AND epoch=? AND id>? ORDER BY id LIMIT ?`, scope, name, epoch, afterID, limit)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out = []skills.WorkflowBucket{}
	for _, id := range ids {
		b, e := readConsumptionBucket(ctx, tx, scope, name, epoch, id)
		if e != nil {
			return nil, e
		}
		if b.Domain != head.Domain {
			return nil, skills.ErrInvalid
		}
		out = append(out, b)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
