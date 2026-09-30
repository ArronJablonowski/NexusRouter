package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestDeprecationEvidenceOriginalWindowAndRevision(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	base := revisionBase(t, db)
	// Additional immutable base records isolate ordering from execution tests.
	add := func(id string, at time.Time) evaluation.Record {
		r := base
		r.ID = id
		r.TaskID = id
		r.AttemptID = id
		r.Time = at
		start := runtime.Event{Version: 1, ID: id + "-start", TaskID: id, SessionID: id, CorrelationID: id, Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
		if err := db.Append(ctx, 0, start); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(r)
		if _, err := db.db.Exec(`INSERT INTO evaluations VALUES(?,?,?,?,?,?,?,?)`, r.ID, r.TaskID, r.AttemptID, r.Key.Model, r.Key.Provider, r.Key.Domain, r.Key.Profile, body); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`INSERT INTO evaluation_heads VALUES(?,?)`, id, id); err != nil {
			t.Fatal(err)
		}
		return r
	}
	newer := add("newer", base.Time.Add(time.Nanosecond))
	tied := add("tie", newer.Time)
	corrected := revised(base, "latest-correction", false)
	if err := db.SupersedeEvaluation(ctx, base.ID, corrected); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 2, 3} {
		got, err := db.DeprecationEvidence(ctx, base.Key, size)
		want := []evaluation.Record{newer, tied, corrected}[:size]
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal(got, err)
		}
	}
	key := base.Key
	key.Profile = "other"
	if got, err := db.DeprecationEvidence(ctx, key, 10); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	before, err := db.EvaluationHistory(ctx, base.TaskID, base.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ro.DeprecationEvidence(ctx, base.Key, 3); err != nil || len(got) != 3 {
		t.Fatal(got, err)
	}
	after, err := db.EvaluationHistory(ctx, base.TaskID, base.AttemptID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection mutated evidence", err)
	}
}

func TestDeprecationEvidenceCorruptionNoPartial(t *testing.T) {
	for _, mode := range []string{"head", "missing_head", "key", "time", "invalid_body", "oversize", "revision", "revision_time", "revision_size"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			base := revisionBase(t, db)
			if err := db.SupersedeEvaluation(ctx, base.ID, revised(base, "rev", false)); err != nil {
				t.Fatal(err)
			}
			query := ""
			switch mode {
			case "head":
				query = `UPDATE evaluation_heads SET current_id='missing'`
			case "missing_head":
				query = `DELETE FROM evaluation_heads`
			case "key":
				query = `UPDATE evaluations SET body=json_set(body,'$.Key.Model','other')`
			case "time":
				query = `UPDATE evaluations SET body=json_set(body,'$.Time','invalid')`
			case "invalid_body":
				query = `UPDATE evaluations SET body='{'`
			case "oversize":
				query = `UPDATE evaluations SET body=json_set(body,'$.padding',printf('%.*c',262145,'x'))`
			case "revision":
				query = `UPDATE evaluation_revisions SET supersedes='missing'`
			case "revision_time":
				query = `UPDATE evaluation_revisions SET body=json_set(body,'$.Time','2026-09-01T00:00:00Z')`
			case "revision_size":
				query = `UPDATE evaluation_revisions SET body=json_set(body,'$.padding',printf('%.*c',262145,'x'))`
			}
			if _, err := db.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			got, err := db.DeprecationEvidence(ctx, base.Key, 10)
			if err == nil || got != nil {
				t.Fatal("corruption returned evidence", got, err)
			}
		})
	}
}

func TestDeprecationEvidenceInvalidAndCanceled(t *testing.T) {
	db, _ := submissionStore(t)
	base := revisionBase(t, db)
	for _, window := range []int{0, -1, 1001} {
		if got, err := db.DeprecationEvidence(context.Background(), base.Key, window); err == nil || got != nil {
			t.Fatal(got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := db.DeprecationEvidence(ctx, base.Key, 10); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatal(got, err)
	}
	if got, err := db.DeprecationEvidence(nil, base.Key, 10); err == nil || got != nil {
		t.Fatal(got, err)
	}
}

func TestDeprecationEvidenceModelTags(t *testing.T) {
	for _, model := range []string{"gemma4:12b", "org/model:tag", strings.Repeat("m", 512)} {
		t.Run(model[:min(len(model), 20)], func(t *testing.T) {
			db, _ := submissionStore(t)
			base := revisionBase(t, db)
			base.Key.Model = model
			body, _ := json.Marshal(base)
			if _, err := db.db.Exec(`UPDATE evaluations SET model=?,body=? WHERE id=?`, model, body, base.ID); err != nil {
				t.Fatal(err)
			}
			got, err := db.DeprecationEvidence(context.Background(), base.Key, 1)
			if err != nil || len(got) != 1 || got[0].Key.Model != model {
				t.Fatal(got, err)
			}
			for _, invalid := range []string{"", "  ", "bad\nmodel", string([]byte{255}), strings.Repeat("m", 513)} {
				key := base.Key
				key.Model = invalid
				if got, err := db.DeprecationEvidence(context.Background(), key, 1); err == nil || got != nil {
					t.Fatal("invalid model accepted")
				}
			}
		})
	}
}
