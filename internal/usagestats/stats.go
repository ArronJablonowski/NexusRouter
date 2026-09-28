// Package usagestats projects durable usage and stores independent trip markers.
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

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("trip changed; refresh before resetting")

type Count struct {
	Input    string `json:"input"`
	Output   string `json:"output"`
	Unknown  int64  `json:"unknown"`
	Measured int64  `json:"measured"`
	Partial  int64  `json:"partial"`
}
type Meter struct {
	Lifetime Count  `json:"lifetime"`
	Trip     Count  `json:"trip"`
	Revision int64  `json:"revision"`
	ResetAt  string `json:"reset_at"`
}
type Snapshot struct {
	Version      int    `json:"version"`
	Cloud        Meter  `json:"cloud"`
	Local        Meter  `json:"local"`
	Unclassified int64  `json:"unclassified"`
	UpdatedAt    string `json:"updated_at"`
}
type Reset struct {
	Version  int    `json:"version"`
	Locality string `json:"locality"`
	Revision int64  `json:"revision"`
	Confirm  bool   `json:"confirm"`
}

func (r Reset) Valid() bool {
	return r.Version == 1 && (r.Locality == "cloud" || r.Locality == "local") && r.Revision >= 0 && r.Confirm
}

// Read uses base-record rowids as trip boundaries, so corrections to older
// usage affect lifetime totals without leaking pre-reset usage into a trip.
// Sidecar transactions serialize resets across processes. The usage database
// is read-only; original accounting records and correction history never change.
func Read(ctx context.Context, path string, locality map[[2]string]string, reset *Reset) (Snapshot, error) {
	var out Snapshot
	if reset != nil && !reset.Valid() {
		return out, errors.New("invalid reset")
	}
	f, err := os.OpenFile(path+".stats.db", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return out, err
	}
	f.Close()
	markers, err := sql.Open("sqlite", path+".stats.db")
	if err != nil {
		return out, err
	}
	defer markers.Close()
	markers.SetMaxOpenConns(1)
	if _, err = markers.ExecContext(ctx, `PRAGMA busy_timeout=3000; CREATE TABLE IF NOT EXISTS trips(locality TEXT PRIMARY KEY, watermark INTEGER NOT NULL, revision INTEGER NOT NULL, reset_at TEXT NOT NULL); INSERT OR IGNORE INTO trips VALUES('cloud',0,0,''),('local',0,0,'');`); err != nil {
		return out, err
	}
	tx, err := markers.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if reset != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE trips SET revision=revision WHERE locality=?", reset.Locality); err != nil {
			return out, err
		}
	}
	type marker struct {
		watermark, revision int64
		at                  string
	}
	bounds := map[string]marker{}
	for _, kind := range []string{"cloud", "local"} {
		var m marker
		if err = tx.QueryRowContext(ctx, "SELECT watermark,revision,reset_at FROM trips WHERE locality=?", kind).Scan(&m.watermark, &m.revision, &m.at); err != nil {
			return out, err
		}
		bounds[kind] = m
	}
	source := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", source)
	if err != nil {
		return out, err
	}
	defer db.Close()
	read, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer read.Rollback()
	var maximum int64
	if err = read.QueryRowContext(ctx, "SELECT coalesce(max(rowid),0) FROM usage_records").Scan(&maximum); err != nil {
		return out, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if reset != nil {
		old := bounds[reset.Locality]
		if old.revision != reset.Revision {
			return out, ErrConflict
		}
		next := marker{maximum, old.revision + 1, now}
		bounds[reset.Locality] = next
		if _, err = tx.ExecContext(ctx, "UPDATE trips SET watermark=?,revision=?,reset_at=? WHERE locality=?", next.watermark, next.revision, next.at, reset.Locality); err != nil {
			return out, err
		}
	}
	rows, err := read.QueryContext(ctx, `SELECT r.rowid,r.body,c.body,h.current_id,r.id FROM usage_records r JOIN usage_heads h ON h.base_id=r.id LEFT JOIN usage_corrections c ON c.id=h.current_id ORDER BY r.rowid`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type totals struct{ in, out, unknown, measured, partial int64 }
	life := map[string]totals{}
	trip := map[string]totals{}
	add := func(t totals, r accounting.Record, partial *providers.Usage) (totals, error) {
		usage := r.Usage
		if usage == nil {
			t.unknown++
			if partial == nil {
				return t, nil
			}
			usage = partial
			t.partial++
		}
		t.measured++
		i, o := usage.InputTokens, usage.OutputTokens
		if i < 0 || o < 0 || t.in > math.MaxInt64-i || t.out > math.MaxInt64-o {
			return t, errors.New("invalid usage totals")
		}
		t.in += i
		t.out += o
		return t, nil
	}
	for rows.Next() {
		var id int64
		var body, correction []byte
		var current, base string
		if err = rows.Scan(&id, &body, &correction, &current, &base); err != nil {
			return out, err
		}
		var record accounting.Record
		if json.Unmarshal(body, &record) != nil {
			return out, errors.New("invalid usage")
		}
		if current != base {
			var c accounting.Correction
			if len(correction) == 0 || json.Unmarshal(correction, &c) != nil {
				return out, errors.New("invalid correction")
			}
			record = c.Record
		}
		kind := locality[[2]string{record.Provider, record.Model}]
		if kind != "cloud" && kind != "local" {
			out.Unclassified++
			continue
		}
		var partial *providers.Usage
		if record.Usage == nil && current == base {
			partial, err = measuredTurns(ctx, read, record)
			if err != nil {
				return out, err
			}
		}
		life[kind], err = add(life[kind], record, partial)
		if err != nil {
			return out, err
		}
		if id > bounds[kind].watermark {
			trip[kind], err = add(trip[kind], record, partial)
			if err != nil {
				return out, err
			}
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	count := func(t totals) Count {
		return Count{strconv.FormatInt(t.in, 10), strconv.FormatInt(t.out, 10), t.unknown, t.measured, t.partial}
	}
	meter := func(kind string) Meter {
		m := bounds[kind]
		return Meter{count(life[kind]), count(trip[kind]), m.revision, m.at}
	}
	out.Version = 1
	out.Cloud = meter("cloud")
	out.Local = meter("local")
	out.UpdatedAt = now
	if err = tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}
