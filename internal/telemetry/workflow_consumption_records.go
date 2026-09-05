package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

const consumptionProjection = `revision,length(CAST(body AS BLOB)),CASE WHEN length(CAST(body AS BLOB))<=524288 THEN body END`

func readConsumption(ctx context.Context, tx *sql.Tx, scope, name string, revision int64, head bool) (skills.WorkflowScanConsumption, error) {
	query := `SELECT ` + consumptionProjection + ` FROM workflow_scan_consumptions WHERE scope=? AND name=? AND revision=?`
	args := []any{scope, name, revision}
	if head {
		query = `SELECT ` + consumptionProjection + ` FROM workflow_scan_consumers WHERE scope=? AND name=?`
		args = args[:2]
	}
	var rev, size int64
	var body []byte
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&rev, &size, &body); err != nil {
		return skills.WorkflowScanConsumption{}, err
	}
	var out skills.WorkflowScanConsumption
	if size < 1 || size > 524288 || int64(len(body)) != size || json.Unmarshal(body, &out) != nil || out.Validate() != nil || out.Scope != scope || out.Name != name || out.Revision != rev || (!head && rev != revision) {
		return out, skills.ErrInvalid
	}
	return out, nil
}

func consumptionLedger(ctx context.Context, tx *sql.Tx, scope, name string, expected int64) error {
	var count, min, max, invalid int64
	err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(min(CASE WHEN typeof(revision)='integer' THEN revision END),0),coalesce(max(CASE WHEN typeof(revision)='integer' THEN revision END),0),coalesce(sum(CASE WHEN typeof(revision)!='integer' THEN 1 ELSE 0 END),0) FROM workflow_scan_consumptions WHERE scope=? AND name=?`, scope, name).Scan(&count, &min, &max, &invalid)
	if err != nil {
		return err
	}
	if invalid != 0 || count != expected || max != expected || (expected > 0 && min != 1) {
		return skills.ErrInvalid
	}
	return nil
}

func consumptionPage(ctx context.Context, tx *sql.Tx, scope, name string, rev int64) (skills.WorkflowScanPage, error) {
	page, err := readWorkflowScanPage(ctx, tx, scope, name, rev)
	if err != nil {
		return page, err
	}
	var prev *skills.WorkflowScan
	if rev > 1 {
		p, e := readWorkflowScanPage(ctx, tx, scope, name, rev-1)
		if e != nil {
			return page, e
		}
		prev = &p.Scan
	}
	if err = page.ValidateAfter(prev); err != nil {
		return page, err
	}
	return page, nil
}

func consumptionDigest(page skills.WorkflowScanPage) string {
	body, _ := json.Marshal(page)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func consumptionBinding(ctx context.Context, tx *sql.Tx, r skills.WorkflowScanConsumption) error {
	p, err := consumptionPage(ctx, tx, r.Scope, r.Name, r.Revision)
	if err != nil {
		return err
	}
	if p.Scan.Epoch != r.Epoch || p.Scan.Domain != r.Domain || consumptionDigest(p) != r.PageDigest || len(p.Page.Candidates) != r.Considered {
		return skills.ErrInvalid
	}
	return nil
}

func consumptionSourceHead(ctx context.Context, tx *sql.Tx, scope, name string) (skills.WorkflowScan, error) {
	h, err := readWorkflowScan(ctx, tx, scope, name)
	if err != nil {
		return h, err
	}
	if err = workflowScanLedger(ctx, tx, scope, name, h.Revision); err != nil {
		return h, err
	}
	p, err := consumptionPage(ctx, tx, scope, name, h.Revision)
	if err != nil {
		return h, err
	}
	if p.Scan != h {
		return h, skills.ErrInvalid
	}
	return h, nil
}

func consumptionHead(ctx context.Context, tx *sql.Tx, scope, name string) (skills.WorkflowScanConsumption, error) {
	h, err := readConsumption(ctx, tx, scope, name, 0, true)
	if err != nil {
		return h, err
	}
	if err = consumptionLedger(ctx, tx, scope, name, h.Revision); err != nil {
		return h, err
	}
	r, err := readConsumption(ctx, tx, scope, name, h.Revision, false)
	if err != nil {
		return h, skills.ErrInvalid
	}
	if !reflect.DeepEqual(h, r) {
		return h, skills.ErrInvalid
	}
	if err = consumptionBinding(ctx, tx, h); err != nil {
		return h, skills.ErrInvalid
	}
	for _, expected := range h.Buckets {
		actual, e := readConsumptionBucket(ctx, tx, scope, name, h.Epoch, expected.ID)
		if e != nil || !reflect.DeepEqual(actual, expected) {
			return h, skills.ErrInvalid
		}
	}
	return h, nil
}

func guardConsumption(ctx context.Context, guard func(context.Context, skills.WorkflowScanConsumption) error, r skills.WorkflowScanConsumption) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if guard == nil || r.Validate() != nil {
		return ErrWorkflowScanUnavailable
	}
	body, err := json.Marshal(r)
	if err != nil || len(body) > 524288 {
		return ErrWorkflowScanUnavailable
	}
	var owned skills.WorkflowScanConsumption
	if json.Unmarshal(body, &owned) != nil {
		return ErrWorkflowScanUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	defer func() {
		if recover() != nil {
			err = ErrWorkflowScanUnavailable
		}
		if bounded.Err() != nil {
			err = ErrWorkflowScanUnavailable
		}
	}()
	if guard(bounded, owned) != nil {
		return ErrWorkflowScanUnavailable
	}
	return nil
}

func readConsumptionBucket(ctx context.Context, tx *sql.Tx, scope, name string, epoch int64, id string) (skills.WorkflowBucket, error) {
	var size, revision int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT revision,length(CAST(body AS BLOB)),CASE WHEN length(CAST(body AS BLOB))<=65536 THEN body END FROM workflow_scan_buckets WHERE scope=? AND name=? AND epoch=? AND id=?`, scope, name, epoch, id).Scan(&revision, &size, &body)
	if err != nil {
		return skills.WorkflowBucket{}, err
	}
	var b skills.WorkflowBucket
	if size < 1 || size > 65536 || int64(len(body)) != size || json.Unmarshal(body, &b) != nil || b.Validate() != nil || b.ID != id {
		return b, skills.ErrInvalid
	}
	head, err := readConsumption(ctx, tx, scope, name, 0, true)
	if err != nil || revision < 1 || revision > head.Revision {
		return b, skills.ErrInvalid
	}
	receipt, err := readConsumption(ctx, tx, scope, name, revision, false)
	if err != nil || receipt.Epoch != epoch || consumptionBinding(ctx, tx, receipt) != nil {
		return b, skills.ErrInvalid
	}
	for _, saved := range receipt.Buckets {
		if saved.ID == id {
			if reflect.DeepEqual(saved, b) {
				return b, nil
			}
			return b, skills.ErrInvalid
		}
	}
	return b, skills.ErrInvalid
}

func consumptionBucketCount(ctx context.Context, tx *sql.Tx, scope, name string, epoch int64) (int, error) {
	var count, invalid int
	err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(CASE WHEN typeof(epoch)!='integer' OR typeof(revision)!='integer' OR revision<1 OR revision>1000000000 OR length(CAST(id AS BLOB))!=64 OR id GLOB '*[^0-9a-f]*' OR length(CAST(body AS BLOB)) NOT BETWEEN 1 AND 65536 THEN 1 ELSE 0 END),0) FROM workflow_scan_buckets WHERE scope=? AND name=? AND epoch=?`, scope, name, epoch).Scan(&count, &invalid)
	if err != nil {
		return 0, err
	}
	if count > 1000 || invalid != 0 {
		return 0, skills.ErrInvalid
	}
	// Re-derive the latest touch from immutable receipts rather than trusting a
	// mutable catalog. This also detects missing buckets and old-valid rollback.
	// The transaction deadline bounds scanning a long receipt history.
	var expected, mismatched int
	var oversized int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_scan_consumptions WHERE scope=? AND name=? AND length(CAST(body AS BLOB)) NOT BETWEEN 1 AND 524288`, scope, name).Scan(&oversized); err != nil {
		return 0, err
	}
	if oversized != 0 {
		return 0, skills.ErrInvalid
	}
	err = tx.QueryRowContext(ctx, `WITH touches AS (
 SELECT json_extract(j.value,'$.id') AS id,max(r.revision) AS revision
 FROM workflow_scan_consumptions r,json_each(CASE WHEN length(CAST(r.body AS BLOB)) BETWEEN 1 AND 524288 THEN r.body ELSE '{}' END,'$.buckets') j
 WHERE r.scope=? AND r.name=? AND json_extract(r.body,'$.epoch')=?
 GROUP BY json_extract(j.value,'$.id'))
 SELECT count(*),coalesce(sum(CASE WHEN b.id IS NULL OR b.revision!=t.revision THEN 1 ELSE 0 END),0)
 FROM touches t LEFT JOIN workflow_scan_buckets b ON b.scope=? AND b.name=? AND b.epoch=? AND b.id=t.id`, scope, name, epoch, scope, name, epoch).Scan(&expected, &mismatched)
	if err != nil {
		return 0, err
	}
	if expected != count || mismatched != 0 {
		return 0, skills.ErrInvalid
	}
	return count, nil
}
