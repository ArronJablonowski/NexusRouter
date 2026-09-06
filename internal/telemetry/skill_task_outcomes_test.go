package telemetry

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"modernc.org/sqlite"
)

func TestSkillTaskOutcomesCanonicalOwnedAndCurrent(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	a := workflowSourceFixture(t, s, "a", "sa", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	workflowSourceFixture(t, s, "b", "sb", "creative", runtime.TaskFailed, evaluation.UserFeedback, false, true)
	next := a
	next.ID = "revised"
	next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "reject", Passed: false}}
	if err := s.SupersedeEvaluation(ctx, a.ID, next); err != nil {
		t.Fatal(err)
	}
	ids := []string{"b", "a"}
	before := workflowSourceRawBodies(t, s)
	out, err := s.SkillTaskOutcomes(ctx, ids)
	if err != nil || len(out) != 2 || out[0].TaskID != "a" || out[1].TaskID != "b" || out[0].Quality.Accepted || out[1].Quality.Accepted || out[0].EvaluationID != "revised" || !reflect.DeepEqual(ids, []string{"b", "a"}) {
		t.Fatal(out, err, ids)
	}
	out[0].Quality.References[0] = "changed"
	again, err := s.SkillTaskOutcomes(ctx, ids)
	if err != nil || again[0].Quality.References[0] != "reject" || !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
		t.Fatal(again, err)
	}
	for _, ids := range [][]string{nil, {}, {"a", "a"}, {"a", "missing"}, {"bad:id"}, make([]string, 201)} {
		if got, err := s.SkillTaskOutcomes(ctx, ids); err == nil || got != nil {
			t.Fatal("invalid batch returned", got, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := s.SkillTaskOutcomes(canceled, []string{"a"}); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatal(got, err)
	}
	if _, err := s.SkillTaskOutcomes(nil, []string{"a"}); err == nil {
		t.Fatal("nil context")
	}
	if _, err := (*Store)(nil).SkillTaskOutcomes(ctx, []string{"a"}); err == nil {
		t.Fatal("nil store")
	}
}

func TestSkillTaskOutcomesAggregateBudgets(t *testing.T) {
	for _, kind := range []string{"events", "evidence", "count"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			for _, id := range []string{"a", "b"} {
				workflowSourceFixture(t, s, id, "s"+id, "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
			}
			switch kind {
			case "events":
				if _, err := s.db.Exec("UPDATE events SET body=json_set(body,'$.data.text',?) WHERE sequence=1", strings.Repeat("x", (4<<20)+1)); err != nil {
					t.Fatal(err)
				}
			case "evidence":
				if _, err := s.db.Exec("UPDATE evaluations SET body=?", strings.Repeat("x", (4<<20)+1)); err != nil {
					t.Fatal(err)
				}
			case "count":
				if _, err := s.db.Exec("UPDATE task_heads SET sequence=10001 WHERE task_id='b'"); err != nil {
					t.Fatal(err)
				}
			}
			if out, err := s.SkillTaskOutcomes(ctx, []string{"a", "b"}); err == nil || out != nil {
				t.Fatal("budget overflow escaped", out, err)
			}
		})
	}
}

func TestSkillTaskOutcomesWALSnapshotAcrossFeedbackRevisions(t *testing.T) {
	s, path := generationStore(t)
	var records []evaluation.Record
	for _, id := range []string{"a", "b"} {
		records = append(records, workflowSourceFixture(t, s, id, "s"+id, "creative", runtime.TaskCompleted, evaluation.LLMJudge, true, true))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	name := fmt.Sprintf("skill_batch_barrier_%d", skillOutcomeBarrier.Add(1))
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
	if _, err := s.db.Exec(`ALTER TABLE events RENAME TO skill_batch_events; CREATE VIEW events AS SELECT id,task_id,sequence,` + name + `(body) AS body FROM skill_batch_events`); err != nil {
		t.Fatal(err)
	}
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	type result struct {
		out []skills.TaskOutcome
		err error
	}
	done := make(chan result, 1)
	go func() { out, err := reader.SkillTaskOutcomes(ctx, []string{"b", "a"}); done <- result{out, err} }()
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
		t.Fatal(early.err)
	case <-ctx.Done():
		t.Fatal("barrier not reached")
	}
	for _, prior := range records {
		next := prior
		next.ID = prior.ID + "-revised"
		next.AllowJudge = false
		next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "operator", Passed: false}}
		if err = writer.SupersedeEvaluation(ctx, prior.ID, next); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	var got result
	select {
	case got = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal("reader did not finish")
	}
	if got.err != nil || len(got.out) != 2 {
		t.Fatal(got)
	}
	for i, item := range got.out {
		if item.EvaluationID != records[i].ID || item.Quality == nil || !item.Quality.Accepted {
			t.Fatal("mixed snapshot", got)
		}
	}
	later, err := reader.SkillTaskOutcomes(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range later {
		if item.EvaluationID != records[i].ID+"-revised" || item.Quality == nil || item.Quality.Accepted {
			t.Fatal("missed revisions", later)
		}
	}
}
