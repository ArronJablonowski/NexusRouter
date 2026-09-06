package telemetry

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"modernc.org/sqlite"
)

func selectionStorePolicy() skills.ComparisonSelectionPolicy {
	return skills.ComparisonSelectionPolicy{Version: 1, Comparison: skills.ComparisonPolicy{Key: skills.Key{Scope: "project", Name: "lookup"}, BaselineVersion: strings.Repeat("a", 32), CandidateVersion: strings.Repeat("b", 32), Execution: routing.Key{Model: "model", Provider: "provider", Domain: "creative", Profile: "default"}, Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: .1}, Privacy: "local_only", TasksPerVersion: 20}
}

func selectionStoreTask(t *testing.T, s *Store, id, version, privacy string, quality *bool) evaluation.Record {
	t.Helper()
	p := selectionStorePolicy()
	digest := strings.Repeat("c", 64)
	if version == p.Comparison.CandidateVersion {
		digest = strings.Repeat("d", 64)
	}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: id + "-" + string(kind), TaskID: id, SessionID: id + "-session", CorrelationID: id, Sequence: int64(i + 1), Time: time.Unix(100, 0).UTC(), Kind: kind}
		if i > 0 {
			e.TurnID = id + "-turn"
			e.AttemptID = id + "-attempt"
		}
		switch kind {
		case runtime.TaskStarted:
			e.Data = runtime.Data{ModelID: "model", ProviderID: "provider", Domain: "creative", Profile: "default", Privacy: privacy, Messages: []providers.Message{{Role: "user", Content: "private skill body"}}, SkillContext: &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: "lookup", Version: version, Digest: digest}}}}
		case runtime.TurnStarted:
			e.Data = runtime.Data{ModelID: "model", ProviderID: "provider"}
		case runtime.TurnCompleted:
			e.Data = runtime.Data{Text: "private output", FinishReason: "stop"}
		}
		if err := s.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	r := evaluation.Record{Version: 1, ID: id + "-quality", TaskID: id, AttemptID: id + "-attempt", Key: p.Comparison.Execution, Time: time.Unix(101, 0).UTC(), ExecutionSucceeded: true}
	if quality != nil {
		r.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user", Passed: *quality}}
		if err := s.RecordEvaluation(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestSkillComparisonSelectionLatestWindowsBeforeQuality(t *testing.T) {
	s, _ := generationStore(t)
	p := selectionStorePolicy()
	yes, no := true, false
	selectionStoreTask(t, s, "old-baseline", p.Comparison.BaselineVersion, p.Privacy, &yes)
	selectionStoreTask(t, s, "old-candidate", p.Comparison.CandidateVersion, p.Privacy, &yes)
	for i := 0; i < 20; i++ {
		selectionStoreTask(t, s, fmt.Sprintf("b-%02d", i), p.Comparison.BaselineVersion, p.Privacy, nil)
		selectionStoreTask(t, s, fmt.Sprintf("c-%02d", i), p.Comparison.CandidateVersion, p.Privacy, &no)
	}
	selectionStoreTask(t, s, "cloud", p.Comparison.BaselineVersion, "cloud_allowed", &yes)
	selectionStoreTask(t, s, "other-version", strings.Repeat("f", 32), p.Privacy, &yes)
	before := workflowSourceRawBodies(t, s)
	report, out, err := s.SkillComparisonSelection(context.Background(), p)
	if err != nil || len(out) != 40 || report.Watermark != 44 || report.Baseline.Selected != 20 || report.Candidate.Selected != 20 || !report.Baseline.HasMore || !report.Candidate.HasMore || report.Baseline.OldestOrdinal != 3 || report.Baseline.NewestOrdinal != 41 || report.Candidate.OldestOrdinal != 4 || report.Candidate.NewestOrdinal != 42 {
		t.Fatal(report, len(out), err)
	}
	if report.Comparison.Baseline.Samples != 0 || report.Comparison.Candidate.Samples != 20 || report.Comparison.Candidate.Accepted != 0 || report.Comparison.Excluded["unknown_quality"] != 20 || report.Comparison.Status != "insufficient_evidence" {
		t.Fatal("outcome-dependent selection", report.Comparison)
	}
	for _, o := range out {
		if o.TaskID == "old-baseline" || o.TaskID == "old-candidate" || o.TaskID == "cloud" || o.TaskID == "other-version" {
			t.Fatal("wrong selected window", o.TaskID)
		}
	}
	if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
		t.Fatal("selector mutated journal")
	}
	p.Privacy = "cloud_allowed"
	cloud, _, err := s.SkillComparisonSelection(context.Background(), p)
	if err != nil || cloud.Baseline.Selected != 1 || cloud.Candidate.Selected != 0 || cloud.Comparison.Baseline.Accepted != 1 {
		t.Fatal(cloud, err)
	}
}

func TestSkillComparisonSelectionGlobalSiblingWithoutExposure(t *testing.T) {
	s, _ := generationStore(t)
	p := selectionStorePolicy()
	yes := true
	selectionStoreTask(t, s, "selected", p.Comparison.BaselineVersion, p.Privacy, &yes)
	sibling := runtime.Event{Version: 1, ID: "sibling-start", TaskID: "sibling", SessionID: "selected-session", CorrelationID: "sibling", Sequence: 1, Time: time.Unix(102, 0).UTC(), Kind: runtime.TaskStarted}
	if err := s.Append(context.Background(), 0, sibling); err != nil {
		t.Fatal(err)
	}
	r, out, err := s.SkillComparisonSelection(context.Background(), p)
	if err != nil || len(out) != 1 || r.Comparison.Excluded["repeated_session"] != 1 || r.Comparison.Baseline.Samples != 0 {
		t.Fatal(r, err)
	}
}

func TestSkillComparisonSelectionDoesNotFilterRunningOrOtherExecution(t *testing.T) {
	s, _ := generationStore(t)
	p := selectionStorePolicy()
	selectionStoreTask(t, s, "other-execution", p.Comparison.BaselineVersion, p.Privacy, nil)
	if _, err := s.db.Exec(`UPDATE events SET body=json_set(body,'$.data.model_id','other-model') WHERE task_id='other-execution' AND sequence IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	start := runtime.Event{Version: 1, ID: "running-start", TaskID: "running", SessionID: "running-session", CorrelationID: "running", Sequence: 1, Time: time.Unix(100, 0).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{Privacy: p.Privacy, SkillContext: &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: "lookup", Version: p.Comparison.CandidateVersion, Digest: strings.Repeat("d", 64)}}}}}
	if err := s.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	r, out, err := s.SkillComparisonSelection(context.Background(), p)
	if err != nil || len(out) != 2 || r.Baseline.Selected != 1 || r.Candidate.Selected != 1 || r.Comparison.Excluded["other_execution"] != 1 || r.Comparison.Excluded["nonfinal_outcome"] != 1 {
		t.Fatal("selection prematurely filtered outcome/execution", r, err)
	}
}

func TestSkillComparisonSelectionEmptyGuardsAndCorruption(t *testing.T) {
	s, _ := generationStore(t)
	p := selectionStorePolicy()
	r, out, err := s.SkillComparisonSelection(context.Background(), p)
	if err != nil || r.Watermark != 0 || r.Comparison != nil || out == nil || len(out) != 0 || r.Validate() != nil {
		t.Fatal(r, out, err)
	}
	for _, ctx := range []context.Context{nil, func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }()} {
		r, out, err = s.SkillComparisonSelection(ctx, p)
		if err == nil || r.Version != 0 || out != nil {
			t.Fatal("invalid context returned report")
		}
	}
	for _, mode := range []string{"digest", "ordinal", "privacy", "version", "aggregate-budget"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := generationStore(t)
			selectionStoreTask(t, db, "one", p.Comparison.BaselineVersion, p.Privacy, nil)
			switch mode {
			case "digest":
				_, err = db.db.Exec(`UPDATE skill_exposures SET digest=?`, strings.Repeat("e", 64))
			case "ordinal":
				_, err = db.db.Exec(`UPDATE workflow_scan_tasks SET seq=2 WHERE task_id='one'`)
			case "privacy":
				_, err = db.db.Exec(`UPDATE events SET body=json_set(body,'$.data.privacy','cloud_allowed') WHERE task_id='one' AND sequence=1`)
			case "version":
				_, err = db.db.Exec(`UPDATE events SET body=json_set(body,'$.data.skill_context.references[0].version',?) WHERE task_id='one' AND sequence=1`, p.Comparison.CandidateVersion)
			case "aggregate-budget":
				selectionStoreTask(t, db, "two", p.Comparison.CandidateVersion, p.Privacy, nil)
				_, err = db.db.Exec(`UPDATE events SET body=json_set(body,'$.data.text',?) WHERE sequence=3`, strings.Repeat("x", (4<<20)+1))
			}
			if err != nil {
				t.Fatal(err)
			}
			report, observed, err := db.SkillComparisonSelection(context.Background(), p)
			if err == nil || report.Version != 0 || observed != nil {
				t.Fatal("corruption/overflow returned partial selection", report, err)
			}
		})
	}
}

var comparisonSelectionBarrier atomic.Uint64

func TestSkillComparisonSelectionWALFeedbackSnapshot(t *testing.T) {
	s, path := generationStore(t)
	p := selectionStorePolicy()
	yes := true
	record := selectionStoreTask(t, s, "selected", p.Comparison.BaselineVersion, p.Privacy, &yes)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	name := fmt.Sprintf("comparison_selection_barrier_%d", comparisonSelectionBarrier.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if first.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE skill_exposures RENAME TO selection_exposure_rows; CREATE VIEW skill_exposures AS SELECT task_id,scope,name,version,` + name + `(digest) AS digest,ordinal,privacy FROM selection_exposure_rows`); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	type result struct {
		report skills.ComparisonSelectionReport
		err    error
	}
	done := make(chan result, 1)
	go func() { r, _, err := reader.SkillComparisonSelection(ctx, p); done <- result{r, err} }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	select {
	case <-entered:
	case early := <-done:
		joined = true
		t.Fatal("selector ended before barrier", early.err)
	case <-ctx.Done():
		t.Fatal("no snapshot barrier", ctx.Err())
	}
	correction := record
	correction.ID = "corrected"
	correction.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "corrected", Passed: false}}
	if err := s.SupersedeEvaluation(ctx, record.ID, correction); err != nil {
		t.Fatal(err)
	}
	close(release)
	var old result
	select {
	case old = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal("selector did not join")
	}
	if old.err != nil || old.report.Comparison == nil || old.report.Comparison.Baseline.Accepted != 1 {
		t.Fatal("mixed feedback snapshot", old.report, old.err)
	}
	newer, _, err := reader.SkillComparisonSelection(ctx, p)
	if err != nil || newer.Comparison.Baseline.Accepted != 0 || newer.Comparison.EvidenceDigest == old.report.Comparison.EvidenceDigest {
		t.Fatal("fresh snapshot missed correction", newer, err)
	}
	body, _ := json.Marshal(newer)
	if strings.Contains(string(body), "private output") {
		t.Fatal("raw output escaped")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("fixture deadline exhausted")
	}
}
