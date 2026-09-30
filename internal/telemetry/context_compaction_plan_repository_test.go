package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/processguard"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func compactionPlanStartFixture(t *testing.T, store *Store) (sessions.ContextCompactionPlanStart, *sessions.SummaryDraft) {
	t.Helper()
	ctx := context.Background()
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
		e := event(string(kind), int64(i+1), kind)
		e.TurnID, e.AttemptID = "turn", "attempt"
		if kind == runtime.TaskStarted {
			e.Data.Messages = []providers.Message{{Role: "user", Content: strings.Repeat("large original request ", 400)}}
		}
		if kind == runtime.TurnCompleted {
			e.Data.Text = strings.Repeat("large original answer ", 400)
		}
		if err := store.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	source, err := store.TaskSnapshot(ctx, "task")
	if err != nil {
		t.Fatal(err)
	}
	request := sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"preserve request"}}}
	_, checkpoint, err := sessions.PrepareContinuation(source, request)
	if err != nil {
		t.Fatal(err)
	}
	attempt := sessions.SummaryAttempt{Version: 1, ID: "summary-a", TaskID: source.TaskID, SourceDigest: checkpoint.SourceDigest,
		Model: "reviewer", Provider: "provider", Status: "started", SourceSequence: source.Sequence, Keep: 1,
		EstimatedCost: .01, StartedAt: time.Unix(200, 0).UTC()}
	draft := &sessions.SummaryDraft{Request: request, Checkpoint: checkpoint, SourceTaskID: attempt.TaskID,
		SourceSequence: attempt.SourceSequence, SourceDigest: attempt.SourceDigest, Model: attempt.Model,
		Usage: &providers.Usage{InputTokens: 10, OutputTokens: 2}, Elapsed: time.Second}
	process, err := processguard.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := runtime.NewContextEngineIdentity("darwin.default", "v1")
	if err != nil {
		t.Fatal(err)
	}
	digest := func(value string) string {
		got, digestErr := sessions.ContextCompactionLiveSuffixDigest([]providers.Message{{Role: "user", Content: value}})
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		return got
	}
	tiers, err := runtime.NewContextTierPlan(digest("stable"), digest("project"), digest("volatile"), []runtime.ContextTier{runtime.ContextTierStable, runtime.ContextTierProject, runtime.ContextTierVolatile})
	if err != nil {
		t.Fatal(err)
	}
	start, err := sessions.SealContextCompactionPlanStart(sessions.ContextCompactionPlanStart{
		OperationID: "compaction-operation", RequestID: "compaction-request", TaskID: attempt.TaskID,
		SourceSequence: attempt.SourceSequence, SourceDigest: attempt.SourceDigest, AttemptID: attempt.ID,
		Model: attempt.Model, Provider: attempt.Provider, Keep: attempt.Keep, EstimatedCost: attempt.EstimatedCost,
		ConfigSnapshot: []byte(`{"mode":"local_only"}`), PolicySnapshot: []byte(`{"egress":"deny"}`),
		Engine: engine, Tiers: tiers, ProcessID: process.ID, StartedAt: attempt.StartedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return start, draft
}

func compactionPlanFactFixture(t *testing.T, start sessions.ContextCompactionPlanStart, previous sessions.ContextCompactionLifecycleFact, kind sessions.ContextCompactionLifecycleKind, plan *runtime.ContextCompactionPlan, sequence int) sessions.ContextCompactionLifecycleFact {
	t.Helper()
	fact := sessions.ContextCompactionLifecycleFact{ID: "compaction-fact-" + string(rune('0'+sequence)), OperationID: start.OperationID,
		Sequence: sequence, PreviousID: previous.ID, Kind: kind, CreatedAt: start.StartedAt.Add(time.Duration(sequence) * time.Second)}
	if plan != nil {
		fact.PlanDigest, fact.SummaryAttemptID, fact.SummaryReviewID = plan.PlanDigest, plan.Compaction.SummaryAttemptID, plan.Compaction.SummaryReviewID
	}
	if kind == sessions.ContextCompactionFailed || kind == sessions.ContextCompactionRevoked {
		fact.Code = "validation_failed"
	}
	sealed, err := sessions.SealContextCompactionLifecycleFact(fact)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestContextCompactionPlanBeginExactReplayAndConflict(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start, _ := compactionPlanStartFixture(t, store)
	state, created, err := store.BeginContextCompactionPlan(ctx, start)
	if err != nil || !created || state.Status != sessions.ContextCompactionStarted || len(state.Facts) != 1 || state.TerminalAttempt != nil {
		t.Fatalf("begin failed: created=%v state=%+v err=%v", created, state, err)
	}
	replayed, created, err := store.BeginContextCompactionPlan(ctx, start)
	if err != nil || created || replayed.StateDigest != state.StateDigest {
		t.Fatalf("exact replay changed state: created=%v err=%v", created, err)
	}
	restarted := start
	restarted.StartedAt = restarted.StartedAt.Add(time.Hour)
	restarted, err = sessions.SealContextCompactionPlanStart(restarted)
	if err != nil {
		t.Fatal(err)
	}
	if got, wasCreated, replayErr := store.BeginContextCompactionPlan(ctx, restarted); replayErr != nil || wasCreated || got.StateDigest != state.StateDigest {
		t.Fatalf("lost-ack replay with new envelope failed: created=%v err=%v", wasCreated, replayErr)
	}
	changed := start
	changed.Model = "other-model"
	changed, err = sessions.SealContextCompactionPlanStart(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.BeginContextCompactionPlan(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request was not a conflict: %v", err)
	}
	var operations, facts, attempts int
	if err = store.db.QueryRow(`SELECT (SELECT count(*) FROM context_compaction_operations),
		(SELECT count(*) FROM context_compaction_plan_facts),(SELECT count(*) FROM summary_attempts)`).Scan(&operations, &facts, &attempts); err != nil || operations != 1 || facts != 1 || attempts != 1 {
		t.Fatalf("begin was not atomic/idempotent: %d %d %d %v", operations, facts, attempts, err)
	}
	freshKey := start
	freshKey.OperationID, freshKey.RequestID, freshKey.AttemptID = "compaction-operation-2", "compaction-request-2", "summary-b"
	freshKey, err = sessions.SealContextCompactionPlanStart(freshKey)
	if err != nil || freshKey.RequestDigest != start.RequestDigest {
		t.Fatalf("semantic request digest depended on keys: %v", err)
	}
	if _, wasCreated, beginErr := store.BeginContextCompactionPlan(ctx, freshKey); beginErr != nil || !wasCreated {
		t.Fatalf("fresh keys for same semantic request rejected: created=%v err=%v", wasCreated, beginErr)
	}
}

func TestContextCompactionPlanConcurrentBeginOneCreated(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "plans.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	start, _ := compactionPlanStartFixture(t, first)
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, store := range []*Store{first, second} {
		wait.Add(1)
		go func(store *Store) {
			defer wait.Done()
			_, created, beginErr := store.BeginContextCompactionPlan(ctx, start)
			results <- result{created, beginErr}
		}(store)
	}
	wait.Wait()
	close(results)
	created := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created count=%d", created)
	}
}

func TestContextCompactionPlanTypedLifecycleAndImmutableEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start, draft := compactionPlanStartFixture(t, store)
	state, _, err := store.BeginContextCompactionPlan(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	attempt := summaryAttemptForCompactionStart(start)
	attempt.Status, attempt.Draft, attempt.FinishedAt = "drafted", draft, start.StartedAt.Add(time.Second)
	if err = store.CompleteSummary(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	draftedState, created, err := store.BeginContextCompactionPlan(ctx, start)
	if err != nil || created || draftedState.Status != sessions.ContextCompactionStarted || draftedState.TerminalAttempt == nil || draftedState.TerminalAttempt.Status != "drafted" {
		t.Fatalf("lost-ack replay omitted terminal draft: created=%v state=%+v err=%v", created, draftedState, err)
	}
	draftDigest, err := sessions.SummaryDraftDigest(*draft)
	if err != nil {
		t.Fatal(err)
	}
	review := sessions.SummaryReview{Version: 2, ID: "compaction-review", AttemptID: start.AttemptID, Decision: "approved",
		Note: "deterministic validation passed", ValidatorID: "project-tests-v1", SourceSequence: start.SourceSequence,
		SourceDigest: start.SourceDigest, DraftDigest: draftDigest, Time: start.StartedAt.Add(2 * time.Second)}
	if err = store.RecordSummaryReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	source, err := store.TaskSnapshot(ctx, start.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, checkpoint, err := sessions.PrepareContinuation(source, draft.Request)
	if err != nil {
		t.Fatal(err)
	}
	tail := []providers.Message{{Role: "user", Content: "volatile request"}}
	checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = start.AttemptID, review.ID
	plan, err := runtime.SealContextCompactionPlan(runtime.ContextCompactionPlan{
		OperationID: start.OperationID, OperationDigest: start.OperationDigest, RequestID: start.RequestID,
		RequestDigest: start.RequestDigest, Compaction: checkpoint, ConfigDigest: start.ConfigDigest, PolicyDigest: start.PolicyDigest,
		Engine: start.Engine, Tiers: start.Tiers, OriginalPrefix: append(append([]providers.Message{}, source.Messages...), tail...),
		ReplacementPrefix: append(replacement, tail...), LiveSuffixBoundary: len(source.Messages) + len(tail), DraftDigest: draftDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := compactionPlanFactFixture(t, start, state.Facts[0], sessions.ContextCompactionPrepared, &plan, 2)
	state, err = store.PrepareContextCompactionPlan(ctx, plan, prepared)
	if err != nil || state.Status != sessions.ContextCompactionPrepared || state.Plan == nil || state.TerminalAttempt == nil {
		t.Fatalf("prepare failed: %+v %v", state, err)
	}
	if replay, replayErr := store.PrepareContextCompactionPlan(ctx, plan, prepared); replayErr != nil || replay.StateDigest != state.StateDigest {
		t.Fatalf("prepare replay failed: %v", replayErr)
	}
	illegal := compactionPlanFactFixture(t, start, prepared, sessions.ContextCompactionApproved, &plan, 3)
	if _, err = store.ApproveContextCompactionPlan(ctx, illegal); !errors.Is(err, ErrConflict) {
		t.Fatalf("illegal prepared -> approved transition: %v", err)
	}
	validated := compactionPlanFactFixture(t, start, prepared, sessions.ContextCompactionValidated, &plan, 3)
	state, err = store.ValidateContextCompactionPlan(ctx, validated)
	if err != nil || state.Status != sessions.ContextCompactionValidated {
		t.Fatalf("validate failed: %v", err)
	}
	approved := compactionPlanFactFixture(t, start, validated, sessions.ContextCompactionApproved, &plan, 4)
	state, err = store.ApproveContextCompactionPlan(ctx, approved)
	if err != nil || state.Status != sessions.ContextCompactionApproved {
		t.Fatalf("approve failed: %v", err)
	}
	if _, err = store.db.Exec("UPDATE context_compaction_plans SET plan_digest=? WHERE operation_id=?", start.SourceDigest, start.OperationID); err == nil {
		t.Fatal("immutable plan was updated")
	}
	if got, readErr := store.ContextCompactionPlan(ctx, start.OperationID); readErr != nil || got.StateDigest != state.StateDigest {
		t.Fatalf("read projection changed: %v", readErr)
	}
}
