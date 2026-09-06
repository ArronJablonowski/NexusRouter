package telemetry

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"modernc.org/sqlite"
)

func TestSkillComparisonSourcesFrozenIDsCurrentEvidence(t *testing.T) {
	for _, change := range []string{"unselected", "feedback", "sibling", "new-evaluation", "missing", "ordinal"} {
		t.Run(change, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			p := selectionStorePolicy()
			yes := true
			quality := &yes
			if change == "new-evaluation" {
				quality = nil
			}
			record := selectionStoreTask(t, s, "selected", p.Comparison.BaselineVersion, p.Privacy, quality)
			report, original, err := s.SkillComparisonSelection(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			if report.Sources == nil || !reflect.DeepEqual(report.Sources.Tasks, []string{"selected"}) {
				t.Fatal(report.Sources)
			}
			report.ConfiguredModelID = "brain"
			report.Comparison.ConfiguredModelID = "brain"
			if out, err := s.CheckSkillComparisonSources(ctx, report); err != nil || !reflect.DeepEqual(out, original) {
				t.Fatal(out, err)
			}
			before, _ := json.Marshal(report)
			switch change {
			case "unselected":
				selectionStoreTask(t, s, "new", p.Comparison.BaselineVersion, p.Privacy, &yes)
			case "feedback":
				correction := record
				correction.ID = "corrected"
				correction.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "corrected", Passed: false}}
				if err = s.SupersedeEvaluation(ctx, record.ID, correction); err != nil {
					t.Fatal(err)
				}
			case "sibling":
				if err = s.Append(ctx, 0, runtime.Event{Version: 1, ID: "sibling-start", TaskID: "sibling", SessionID: "selected-session", CorrelationID: "sibling", Sequence: 1, Time: time.Unix(102, 0).UTC(), Kind: runtime.TaskStarted}); err != nil {
					t.Fatal(err)
				}
			case "new-evaluation":
				record.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user", Passed: true}}
				if err = s.RecordEvaluation(ctx, record); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if _, err = s.db.Exec("DELETE FROM events WHERE task_id='selected'"); err != nil {
					t.Fatal(err)
				}
			case "ordinal":
				if _, err = s.db.Exec("UPDATE workflow_scan_tasks SET seq=20 WHERE task_id='selected'"); err != nil {
					t.Fatal(err)
				}
			}
			raw := workflowSourceRawBodies(t, s)
			out, err := s.CheckSkillComparisonSources(ctx, report)
			if change == "unselected" {
				if err != nil || !reflect.DeepEqual(out, original) {
					t.Fatal(out, err)
				}
			} else if err == nil || out != nil {
				t.Fatal("changed evidence accepted", out, err)
			}
			after, _ := json.Marshal(report)
			if string(before) != string(after) {
				t.Fatal("input mutated")
			}
			if !reflect.DeepEqual(raw, workflowSourceRawBodies(t, s)) {
				t.Fatal("source check mutated journal")
			}
		})
	}
}

func TestSkillComparisonSourcesWALSnapshot(t *testing.T) {
	s, path := generationStore(t)
	p := selectionStorePolicy()
	yes := true
	prior := selectionStoreTask(t, s, "selected", p.Comparison.BaselineVersion, p.Privacy, &yes)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, _, err := s.SkillComparisonSelection(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	name := fmt.Sprintf("skill_sources_barrier_%d", skillOutcomeBarrier.Add(1))
	if err = sqlite.RegisterScalarFunction(name, 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if args[0] == int64(4) && first.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return args[1], nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`ALTER TABLE events RENAME TO source_check_events; CREATE VIEW events AS SELECT id,task_id,sequence,` + name + `(sequence,body) AS body FROM source_check_events`); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	done := make(chan error, 1)
	go func() { _, e := reader.CheckSkillComparisonSources(ctx, report); done <- e }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	select {
	case <-entered:
	case e := <-done:
		joined = true
		t.Fatal("reader exited before barrier", e)
	case <-ctx.Done():
		t.Fatal("barrier timeout")
	}
	next := prior
	next.ID = "corrected"
	next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "corrected", Passed: false}}
	if err = s.SupersedeEvaluation(ctx, prior.ID, next); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal("reader did not join")
	}
	if err != nil {
		t.Fatal("mixed WAL snapshot", err)
	}
	if out, e := reader.CheckSkillComparisonSources(ctx, report); !errors.Is(e, skills.ErrConflict) || out != nil {
		t.Fatal("fresh snapshot missed correction", out, e)
	}
}

func TestSkillComparisonSourcesEmptyAndGuards(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	p := selectionStorePolicy()
	r, _, err := s.SkillComparisonSelection(ctx, p)
	if err != nil || r.Sources == nil || r.Sources.Tasks == nil || len(r.Sources.Tasks) != 0 {
		t.Fatal(r, err)
	}
	yes := true
	selectionStoreTask(t, s, "later", p.Comparison.BaselineVersion, p.Privacy, &yes)
	out, err := s.CheckSkillComparisonSources(ctx, r)
	if err != nil || out == nil || len(out) != 0 {
		t.Fatal(out, err)
	}
	legacy := r
	legacy.Sources = nil
	if _, err = s.CheckSkillComparisonSources(ctx, legacy); !errors.Is(err, skills.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = s.CheckSkillComparisonSources(nil, r); !errors.Is(err, skills.ErrInvalid) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.CheckSkillComparisonSources(canceled, r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var missing *Store
	if _, err = missing.CheckSkillComparisonSources(ctx, r); !errors.Is(err, skills.ErrInvalid) {
		t.Fatal(err)
	}
}
