package usagestats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/accounting"
)

// RemoteUsage contains only measured counters, never prompts, responses or credentials.
type RemoteUsage struct {
	Local        Count     `json:"local"`
	Cloud        Count     `json:"cloud"`
	Unclassified int64     `json:"unclassified"`
	Unavailable  int64     `json:"unavailable"`
	ObservedAt   time.Time `json:"observed_at"`
}
type RemoteMeter struct {
	Total Count `json:"total"`
	RemoteUsage
	Requests  int64  `json:"requests"`
	Pending   int64  `json:"pending"`
	LastSync  string `json:"last_sync"`
	SyncError bool   `json:"sync_error"`
}

func emptyCount() Count { return Count{Input: "0", Output: "0"} }
func EmptyRemoteUsage() RemoteUsage {
	return RemoteUsage{Local: emptyCount(), Cloud: emptyCount(), ObservedAt: time.Now().UTC()}
}
func addCount(a, b Count) (Count, error) {
	ai, e := strconv.ParseInt(a.Input, 10, 64)
	if e != nil {
		return a, e
	}
	ao, e := strconv.ParseInt(a.Output, 10, 64)
	if e != nil {
		return a, e
	}
	bi, e := strconv.ParseInt(b.Input, 10, 64)
	if e != nil {
		return a, e
	}
	bo, e := strconv.ParseInt(b.Output, 10, 64)
	if e != nil {
		return a, e
	}
	values := [][2]int64{{ai, bi}, {ao, bo}, {a.Unknown, b.Unknown}, {a.Measured, b.Measured}, {a.Partial, b.Partial}}
	for _, v := range values {
		if v[0] < 0 || v[1] < 0 || v[0] > math.MaxInt64-v[1] {
			return a, errors.New("invalid remote usage")
		}
	}
	return Count{Input: strconv.FormatInt(ai+bi, 10), Output: strconv.FormatInt(ao+bo, 10), Unknown: a.Unknown + b.Unknown, Measured: a.Measured + b.Measured, Partial: a.Partial + b.Partial}, nil
}
func (u RemoteUsage) Valid() bool {
	_, a := addCount(emptyCount(), u.Local)
	_, b := addCount(emptyCount(), u.Cloud)
	return a == nil && b == nil && u.Unclassified >= 0 && u.Unavailable >= 0 && !u.ObservedAt.IsZero() && u.Local.Partial <= u.Local.Unknown && u.Cloud.Partial <= u.Cloud.Unknown && u.Local.Partial <= u.Local.Measured && u.Cloud.Partial <= u.Cloud.Measured
}

// TaskRemoteUsage projects current immutable accounting heads for caller-owned
// task IDs, including auxiliary operations and failed/fallback lineages.
func TaskRemoteUsage(ctx context.Context, path string, locality map[[2]string]string, tasks []string) (RemoteUsage, error) {
	out := EmptyRemoteUsage()
	if len(tasks) > 128 {
		return out, errors.New("too many remote tasks")
	}
	db, e := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if e != nil {
		return out, e
	}
	defer db.Close()
	tx, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	for _, task := range tasks {
		if seen[task] {
			continue
		}
		seen[task] = true
		rows, e := tx.QueryContext(ctx, `SELECT r.body,c.body,h.current_id,r.id FROM usage_records r JOIN usage_heads h ON h.base_id=r.id LEFT JOIN usage_corrections c ON c.id=h.current_id WHERE r.task_id=?`, task)
		if e != nil {
			return out, e
		}
		type row struct {
			body, correction []byte
			current, base    string
		}
		var records []row
		for rows.Next() {
			var r row
			if e = rows.Scan(&r.body, &r.correction, &r.current, &r.base); e != nil {
				rows.Close()
				return out, e
			}
			records = append(records, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		if len(records) == 0 {
			out.Unavailable++
		}
		for _, r := range records {
			var record accounting.Record
			if e = json.Unmarshal(r.body, &record); e != nil {
				return out, e
			}
			if r.current != r.base {
				var c accounting.Correction
				if e = json.Unmarshal(r.correction, &c); e != nil {
					return out, e
				}
				record = c.Record
			}
			if record.Validate() != nil || record.TaskID != task {
				return out, errors.New("invalid remote accounting record")
			}
			kind := locality[[2]string{record.Provider, record.Model}]
			if kind != "local" && kind != "cloud" {
				out.Unclassified++
				continue
			}
			c := emptyCount()
			usage := record.Usage
			if usage == nil {
				c.Unknown = 1
				if r.current == r.base {
					usage, e = measuredTurns(ctx, tx, record)
					if e != nil {
						return out, e
					}
					if usage != nil {
						c.Partial = 1
					}
				}
			}
			if usage != nil {
				c.Measured = 1
				c.Input = strconv.FormatInt(usage.InputTokens, 10)
				c.Output = strconv.FormatInt(usage.OutputTokens, 10)
			}
			if kind == "local" {
				out.Local, e = addCount(out.Local, c)
			} else {
				out.Cloud, e = addCount(out.Cloud, c)
			}
			if e != nil {
				return out, e
			}
		}
	}
	return out, nil
}

func usageDB(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("missing remote usage path")
	}
	if st, e := os.Lstat(path); e == nil && (st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
		return nil, errors.New("unsafe remote usage file")
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA busy_timeout=3000; CREATE TABLE IF NOT EXISTS remote_receipts(destination TEXT NOT NULL,caller TEXT NOT NULL,request TEXT NOT NULL,state TEXT NOT NULL,observed INTEGER NOT NULL,body BLOB NOT NULL,PRIMARY KEY(destination,caller,request)); CREATE TABLE IF NOT EXISTS remote_sync(id INTEGER PRIMARY KEY CHECK(id=1),at TEXT NOT NULL,failed INTEGER NOT NULL);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return db, nil
}

// SaveRemoteUsage replaces a cumulative receipt; repeated polling never adds it
// twice. Stale responses cannot overwrite newer corrections or measurements.
func SaveRemoteUsage(ctx context.Context, path, destination, caller, request, state string, u RemoteUsage) error {
	if !u.Valid() {
		return errors.New("invalid remote usage")
	}
	db, e := usageDB(path)
	if e != nil {
		return e
	}
	defer db.Close()
	body, e := json.Marshal(u)
	if e != nil {
		return e
	}
	_, e = db.ExecContext(ctx, `INSERT INTO remote_receipts VALUES(?,?,?,?,?,?) ON CONFLICT(destination,caller,request) DO UPDATE SET state=excluded.state,observed=excluded.observed,body=excluded.body WHERE excluded.observed>remote_receipts.observed`, destination, caller, request, state, u.ObservedAt.UnixNano(), body)
	return e
}
func MarkRemoteSync(ctx context.Context, path string, failed bool) error {
	db, e := usageDB(path)
	if e != nil {
		return e
	}
	defer db.Close()
	_, e = db.ExecContext(ctx, `INSERT INTO remote_sync VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET at=excluded.at,failed=excluded.failed`, time.Now().UTC().Format(time.RFC3339), failed)
	return e
}
func ReadRemoteUsage(ctx context.Context, path string) (RemoteMeter, error) {
	out := RemoteMeter{RemoteUsage: EmptyRemoteUsage()}
	db, e := usageDB(path)
	if e != nil {
		return out, e
	}
	defer db.Close()
	rows, e := db.QueryContext(ctx, `SELECT state,body FROM remote_receipts`)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var state string
		var body []byte
		if e = rows.Scan(&state, &body); e != nil {
			rows.Close()
			return out, e
		}
		var u RemoteUsage
		if json.Unmarshal(body, &u) != nil || !u.Valid() {
			rows.Close()
			return out, errors.New("invalid remote receipt")
		}
		out.Local, e = addCount(out.Local, u.Local)
		if e == nil {
			out.Cloud, e = addCount(out.Cloud, u.Cloud)
		}
		if e != nil {
			rows.Close()
			return out, e
		}
		if out.Unavailable > math.MaxInt64-u.Unavailable || out.Unclassified > math.MaxInt64-u.Unclassified {
			rows.Close()
			return out, errors.New("remote usage overflow")
		}
		out.Unavailable += u.Unavailable
		out.Unclassified += u.Unclassified
		out.Requests++
		if state == "queued" || state == "running" || state == "unknown" {
			out.Pending++
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	out.Total, e = addCount(out.Local, out.Cloud)
	if e == nil {
		out.Total, e = addCount(out.Total, Count{Input: "0", Output: "0", Unknown: out.Unavailable})
	}
	if e == nil {
		out.Total, e = addCount(out.Total, Count{Input: "0", Output: "0", Unknown: out.Unclassified})
	}
	if e != nil {
		return out, e
	}
	e = db.QueryRowContext(ctx, `SELECT at,failed FROM remote_sync WHERE id=1`).Scan(&out.LastSync, &out.SyncError)
	if e == sql.ErrNoRows {
		e = nil
	}
	return out, e
}

// An unavailable peer must not erase an earlier measured receipt.
func SaveMissingRemoteUsage(ctx context.Context, path, destination, caller, request, state string) error {
	db, e := usageDB(path)
	if e != nil {
		return e
	}
	defer db.Close()
	u := EmptyRemoteUsage()
	u.Unavailable = 1
	body, e := json.Marshal(u)
	if e != nil {
		return e
	}
	_, e = db.ExecContext(ctx, `INSERT OR IGNORE INTO remote_receipts VALUES(?,?,?,?,?,?)`, destination, caller, request, state, u.ObservedAt.UnixNano(), body)
	return e
}
