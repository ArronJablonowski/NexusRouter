package telemetry

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	darwinruntime "github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"modernc.org/sqlite"
)

const contextCompactionCrashFunction = "darwin_test_pause_compaction_activation"

type contextCompactionCrashFixture struct {
	Database string                                  `json:"database"`
	Mode     string                                  `json:"mode"`
	Plan     darwinruntime.ContextCompactionPlan     `json:"plan"`
	Event    darwinruntime.Event                     `json:"event"`
	Fact     sessions.ContextCompactionLifecycleFact `json:"fact"`
}

// TestContextCompactionActivationCrashHelper is executed only in a child test
// process. The before_commit mode stops inside SQLite's activation transaction;
// after_commit stops after AppendContextCompaction returned but before the
// caller can observe a normal process exit.
func TestContextCompactionActivationCrashHelper(t *testing.T) {
	if os.Getenv("DARWIN_COMPACTION_CRASH_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body, err := os.ReadFile(os.Getenv("DARWIN_COMPACTION_CRASH_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture contextCompactionCrashFixture
	if json.Unmarshal(body, &fixture) != nil || fixture.Database == "" || (!strings.HasPrefix(fixture.Mode, "plan_") && fixture.Event.ID == "") {
		t.Fatal("invalid compaction crash fixture")
	}
	beforeCommit := fixture.Mode == "before_commit" || fixture.Mode == "plan_before_commit"
	if beforeCommit {
		if err = sqlite.RegisterScalarFunction(contextCompactionCrashFunction, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			fmt.Println(fixture.Mode)
			<-ctx.Done()
			return nil, errors.New("fixture was not killed")
		}); err != nil {
			t.Fatal(err)
		}
		raw, openErr := sql.Open("sqlite", fixture.Database)
		if openErr != nil {
			t.Fatal(openErr)
		}
		trigger := `CREATE TRIGGER crash_context_compaction_activation
			BEFORE INSERT ON events
			WHEN json_extract(NEW.body,'$.kind')='context.compacted'
			BEGIN SELECT ` + contextCompactionCrashFunction + `(); END`
		if fixture.Mode == "plan_before_commit" {
			trigger = `CREATE TRIGGER crash_context_compaction_activation
				BEFORE INSERT ON context_compaction_plan_facts
				WHEN NEW.sequence=2
				BEGIN SELECT ` + contextCompactionCrashFunction + `(); END`
		}
		_, err = raw.ExecContext(ctx, trigger)
		raw.Close()
		if err != nil {
			t.Fatal(err)
		}
	} else if fixture.Mode != "after_commit" && fixture.Mode != "plan_after_commit" {
		t.Fatal("unsupported compaction crash mode")
	}
	store, err := Open(ctx, fixture.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if strings.HasPrefix(fixture.Mode, "plan_") {
		if _, err = store.PrepareContextCompactionPlan(ctx, fixture.Plan, fixture.Fact); err != nil {
			t.Fatal(err)
		}
	} else {
		if err = store.AppendContextCompaction(ctx, fixture.Event.Sequence-1, fixture.Event, fixture.Plan); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println(fixture.Mode)
	<-ctx.Done()
	t.Fatal("fixture was not killed after activation commit")
}

func unpreparedContextCompactionPlanFixture(t *testing.T, store *Store) (darwinruntime.ContextCompactionPlan, sessions.ContextCompactionLifecycleFact, sessions.ContextCompactionOperationState) {
	t.Helper()
	ctx := context.Background()
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
	draftDigest, err := sessions.SummaryDraftDigest(*draft)
	if err != nil {
		t.Fatal(err)
	}
	review := sessions.SummaryReview{Version: 2, ID: "process-review", AttemptID: start.AttemptID, Decision: "approved",
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
	plan, err := darwinruntime.SealContextCompactionPlan(darwinruntime.ContextCompactionPlan{
		OperationID: start.OperationID, OperationDigest: start.OperationDigest, RequestID: start.RequestID,
		RequestDigest: start.RequestDigest, Compaction: checkpoint, ConfigDigest: start.ConfigDigest, PolicyDigest: start.PolicyDigest,
		Engine: start.Engine, Tiers: start.Tiers, OriginalPrefix: append(append([]providers.Message{}, source.Messages...), tail...),
		ReplacementPrefix: append(replacement, tail...), LiveSuffixBoundary: len(source.Messages) + len(tail), DraftDigest: draftDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := compactionPlanFactFixture(t, start, state.Facts[0], sessions.ContextCompactionPrepared, &plan, 2)
	return plan, prepared, state
}

func TestContextCompactionPlanPreparationSurvivesSIGKILLBoundaries(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	for _, mode := range []string{"plan_before_commit", "plan_after_commit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "plan-crash.db")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			plan, prepared, started := unpreparedContextCompactionPlanFixture(t, store)
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			fixturePath := filepath.Join(t.TempDir(), "fixture.json")
			body, err := json.Marshal(contextCompactionCrashFixture{Database: path, Mode: mode, Plan: plan, Fact: prepared})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(fixturePath, body, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestContextCompactionActivationCrashHelper$", "-test.count=1")
			cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_COMPACTION_CRASH_HELPER=1", "DARWIN_COMPACTION_CRASH_FIXTURE=" + fixturePath}
			cmd.WaitDelay, cmd.Stderr = time.Second, io.Discard
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(stdout)
			marker := make(chan string, 1)
			scanned := make(chan struct{})
			go func() {
				defer close(scanned)
				if scanner.Scan() {
					marker <- strings.TrimSpace(scanner.Text())
					return
				}
				marker <- ""
			}()
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				<-scanned
			}()
			select {
			case got := <-marker:
				if got != mode {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatal("wrong plan crash marker", got)
				}
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("helper did not reach plan crash boundary")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil || cmd.ProcessState == nil {
				t.Fatal("plan crash helper exited gracefully")
			}
			waited = true
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("plan crash helper did not die by SIGKILL", cmd.ProcessState)
			}
			reopened, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if mode == "plan_before_commit" {
				if _, err = reopened.db.ExecContext(ctx, `DROP TRIGGER crash_context_compaction_activation`); err != nil {
					t.Fatal(err)
				}
			}
			state, err := reopened.ContextCompactionPlan(ctx, plan.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "plan_before_commit" && (state.Status != sessions.ContextCompactionStarted || state.Plan != nil || len(state.Facts) != len(started.Facts)) {
				t.Fatal("pre-commit death partially prepared plan", state.Status, state.Plan != nil, len(state.Facts))
			}
			if mode == "plan_after_commit" && (state.Status != sessions.ContextCompactionPrepared || state.Plan == nil || state.Plan.PlanDigest != plan.PlanDigest || len(state.Facts) != len(started.Facts)+1 || state.Facts[len(state.Facts)-1].Kind != sessions.ContextCompactionPrepared) {
				t.Fatal("committed prepared plan was lost", state.Status, state.Plan != nil, len(state.Facts))
			}
			state, err = reopened.PrepareContextCompactionPlan(ctx, plan, prepared)
			if err != nil || state.Status != sessions.ContextCompactionPrepared || state.Plan == nil || state.Plan.PlanDigest != plan.PlanDigest {
				t.Fatal("restart did not reconcile exact prepared plan", state.Status, err)
			}
			var plans, preparedFacts int
			if err = reopened.db.QueryRowContext(ctx, `SELECT
				(SELECT count(*) FROM context_compaction_plans WHERE operation_id=?),
				(SELECT count(*) FROM context_compaction_plan_facts WHERE operation_id=? AND sequence=2)`,
				plan.OperationID, plan.OperationID).Scan(&plans, &preparedFacts); err != nil || plans != 1 || preparedFacts != 1 {
				t.Fatal("restart duplicated prepared plan", plans, preparedFacts, err)
			}
		})
	}
}

func TestContextCompactionActivationSurvivesSIGKILLBoundaries(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	for _, mode := range []string{"before_commit", "after_commit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "compaction-crash.db")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			plan, approved := approvedContextCompactionPlanFixture(t, store)
			event := planActivationEventFixture(t, store, plan, "activation-crash-task")
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			fixturePath := filepath.Join(t.TempDir(), "fixture.json")
			fixtureBody, err := json.Marshal(contextCompactionCrashFixture{Database: path, Mode: mode, Plan: plan, Event: event})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(fixturePath, fixtureBody, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestContextCompactionActivationCrashHelper$", "-test.count=1")
			cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_COMPACTION_CRASH_HELPER=1", "DARWIN_COMPACTION_CRASH_FIXTURE=" + fixturePath}
			cmd.WaitDelay = time.Second
			cmd.Stderr = io.Discard
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			line := make(chan string, 1)
			scanned := make(chan struct{})
			go func() {
				defer close(scanned)
				scanner := bufio.NewScanner(stdout)
				if scanner.Scan() {
					line <- strings.TrimSpace(scanner.Text())
					return
				}
				line <- ""
			}()
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				<-scanned
			}()
			select {
			case marker := <-line:
				if marker != mode {
					t.Fatal("wrong crash marker", marker)
				}
			case <-ctx.Done():
				t.Fatal("helper did not reach compaction crash boundary")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if err == nil || cmd.ProcessState == nil {
				t.Fatal("compaction crash helper exited gracefully")
			}
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("compaction crash helper did not die by SIGKILL", cmd.ProcessState)
			}

			reopened, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if mode == "before_commit" {
				if _, err = reopened.db.ExecContext(ctx, `DROP TRIGGER crash_context_compaction_activation`); err != nil {
					t.Fatal(err)
				}
			}
			state, err := reopened.ContextCompactionPlan(ctx, plan.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "before_commit" {
				if state.Status != sessions.ContextCompactionApproved || len(state.Facts) != len(approved.Facts) {
					t.Fatal("pre-commit death partially activated plan", state.Status, len(state.Facts))
				}
				snapshot, snapshotErr := reopened.TaskSnapshot(ctx, event.TaskID)
				if snapshotErr != nil || snapshot.Sequence != event.Sequence-1 || snapshot.Compaction != nil {
					t.Fatal("pre-commit death changed task head", snapshot, snapshotErr)
				}
			} else if state.Status != sessions.ContextCompactionActivated {
				t.Fatal("committed activation was lost", state.Status)
			}
			if err = reopened.AppendContextCompaction(ctx, event.Sequence-1, event, plan); err != nil {
				t.Fatal("restart retry did not reconcile exact activation", err)
			}
			state, err = reopened.ContextCompactionPlan(ctx, plan.OperationID)
			if err != nil || state.Status != sessions.ContextCompactionActivated {
				t.Fatal("activation missing after recovery", state.Status, err)
			}
			activated := 0
			for _, fact := range state.Facts {
				if fact.Kind == sessions.ContextCompactionActivated {
					activated++
				}
			}
			page, err := reopened.ReadEventPage(ctx, event.TaskID, 0, 100)
			if err != nil || page.HasMore {
				t.Fatal(err, page.HasMore)
			}
			compactions := 0
			for _, item := range page.Events {
				if item.Kind == darwinruntime.ContextCompacted {
					compactions++
				}
			}
			if activated != 1 || compactions != 1 || page.HeadSequence != event.Sequence {
				t.Fatal("restart duplicated or lost activation", activated, compactions, page.HeadSequence)
			}
		})
	}
}
