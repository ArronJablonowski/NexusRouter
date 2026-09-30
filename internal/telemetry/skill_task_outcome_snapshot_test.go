package telemetry

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
	"modernc.org/sqlite"
)

var skillOutcomeBarrier atomic.Uint64

func TestSkillTaskOutcomeWALSnapshotDuringFeedbackRevision(t *testing.T) {
	s, path := generationStore(t)
	prior := workflowSourceFixture(t, s, "task", "session", "creative", runtime.TaskCompleted, evaluation.LLMJudge, true, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	name := fmt.Sprintf("skill_outcome_barrier_%d", skillOutcomeBarrier.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
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
	// The head read has already pinned the read transaction when the journal
	// preflight reaches this final event. Interpose only in this test database;
	// the production operation has no hooks and the actual feedback writer is
	// unchanged. Its evaluation-head commit must not enter the pinned snapshot.
	if _, err := s.db.Exec(`ALTER TABLE events RENAME TO skill_outcome_events; CREATE VIEW events AS SELECT id,task_id,sequence,` + name + `(sequence,body) AS body FROM skill_outcome_events`); err != nil {
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
		out skills.TaskOutcome
		err error
	}
	done := make(chan result, 1)
	go func() { out, err := reader.SkillTaskOutcome(ctx, prior.TaskID); done <- result{out, err} }()
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
		t.Fatal("reader exited before barrier", early.err)
	case <-ctx.Done():
		t.Fatal("barrier not reached")
	}
	next := prior
	next.ID = "revised"
	next.AllowJudge = false
	next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "operator", Passed: false}}
	if err = writer.SupersedeEvaluation(ctx, prior.ID, next); err != nil {
		t.Fatal("concurrent WAL feedback commit failed", err)
	}
	close(release)
	var got result
	select {
	case got = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal("reader did not finish")
	}
	if got.err != nil || got.out.EvaluationID != prior.ID || got.out.Quality == nil || got.out.Quality.Source != evaluation.LLMJudge || !got.out.Quality.Accepted {
		t.Fatal("reader mixed old journal and new quality", got)
	}
	later, err := reader.SkillTaskOutcome(ctx, prior.TaskID)
	if err != nil || later.EvaluationID != next.ID || later.Quality == nil || later.Quality.Source != evaluation.UserFeedback || later.Quality.Accepted || later.EvaluationDigest == got.out.EvaluationDigest {
		t.Fatal("new observation missed feedback revision", later, err)
	}
}
