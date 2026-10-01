package app

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestNativeRecoveryRestoresUnacknowledgedCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	svc, db, providerCalls := recoveryFixture(t)
	claim := recoveryClaim(t, svc, db)
	identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "local", Model: "fixture", ModelRevision: "weights", ConfigSHA256: strings.Repeat("a", 64)}
	calls := 0
	journal := redactingJournal{db: db, submissionID: claim.Status.ID, submissionToken: claim.Token}
	_, text, err := runtime.RunHarness(ctx, journal, runtime.HarnessRequest{TaskID: "native-recovery", SessionID: "native-recovery", SubmissionID: claim.Status.ID, Messages: []providers.Message{{Role: "user", Content: "fixture"}}, Privacy: "local_only", ContextTokens: 8192, MaxOutputBytes: 4096, Attribution: runtime.HarnessAttribution{Identity: identity, Task: harness.TaskClass{Domain: "writing", Profile: "fixture-v1", Difficulty: "unknown"}}, Execute: func(context.Context) (runtime.HarnessOutput, error) {
		calls++
		return runtime.HarnessOutput{Actual: identity, Text: "durable answer", Usage: &providers.Usage{InputTokens: 10, OutputTokens: 4}}, nil
	}})
	if err != nil || text != "durable answer" {
		t.Fatal(text, err)
	}
	before, err := db.Read(ctx, "native-recovery", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	status, err := svc.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "running" || status.Result != nil {
		t.Fatal("fixture must omit final dispatcher acknowledgement", status, err)
	}
	if changed, err := db.RecoverTerminalSubmission(ctx, claim.Status.ID, svc.submissionConfigDigest(), time.Now().UTC()); err != nil || changed {
		t.Fatal("live claim recovered", changed, err)
	}
	expireRecoveryClaim(t, svc, claim.Status.ID)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := telemetry.Open(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if changed, err := reopened.RecoverTerminalSubmission(ctx, claim.Status.ID, strings.Repeat("0", 64), time.Now().UTC()); err != nil || changed {
		t.Fatal("changed authority recovered", changed, err)
	}
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	status = awaitSubmission(t, ctx, svc, claim.Status.ID, "succeeded")
	if err := dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || providerCalls.Load() != 0 || status.Result == nil || status.Result.Text != "durable answer" || status.Result.Turns != 1 || status.Result.FinishReason != "stop" || status.Result.Usage == nil || status.Result.Usage.InputTokens != 10 || status.Result.AuditStatus != "not_recovered" {
		t.Fatal(status, calls, providerCalls.Load())
	}
	after, err := reopened.Read(ctx, "native-recovery", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("recovery changed canonical history", err)
	}
	if changed, err := reopened.RecoverTerminalSubmission(ctx, claim.Status.ID, svc.submissionConfigDigest(), time.Now().UTC()); err != nil || changed {
		t.Fatal("duplicate recovery", changed, err)
	}
	history, err := reopened.RecoveryHistory(ctx, claim.Status.ID)
	if err != nil || len(history) != 1 || history[0].Reason != "terminal_history" {
		t.Fatal(history, err)
	}
}

type interruptedNativeJournal struct{ runtime.Journal }

func (j interruptedNativeJournal) Append(ctx context.Context, sequence int64, event runtime.Event) error {
	if sequence > 0 {
		return context.Canceled
	} // Fixture loses the terminal commit only.
	return j.Journal.Append(ctx, sequence, event)
}

func TestNativeRecoveryDoesNotReplayUncommittedOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	svc, db, providerCalls := recoveryFixture(t)
	claim := recoveryClaim(t, svc, db)
	identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "local", Model: "fixture", ModelRevision: "weights", ConfigSHA256: strings.Repeat("a", 64)}
	calls := 0
	journal := interruptedNativeJournal{redactingJournal{db: db, submissionID: claim.Status.ID, submissionToken: claim.Token}}
	_, _, err := runtime.RunHarness(ctx, journal, runtime.HarnessRequest{TaskID: "native-interrupted", SessionID: "native-interrupted", SubmissionID: claim.Status.ID, Messages: []providers.Message{{Role: "user", Content: "fixture"}}, Privacy: "local_only", ContextTokens: 8192, MaxOutputBytes: 4096, Attribution: runtime.HarnessAttribution{Identity: identity, Task: harness.TaskClass{Domain: "writing", Profile: "fixture-v1", Difficulty: "unknown"}}, Execute: func(context.Context) (runtime.HarnessOutput, error) {
		calls++
		return runtime.HarnessOutput{Actual: identity, Text: "uncommitted answer"}, nil
	}})
	if err == nil {
		t.Fatal("terminal commit unexpectedly succeeded")
	}
	before, err := db.Read(ctx, "native-interrupted", 0, 100)
	if err != nil || len(before) != 1 {
		t.Fatal(before, err)
	}
	expireRecoveryClaim(t, svc, claim.Status.ID)
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	status := awaitSubmission(t, ctx, svc, claim.Status.ID, "failed")
	if err := dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := db.Read(ctx, "native-interrupted", 0, 100)
	if err != nil || len(after) != 2 || !reflect.DeepEqual(after[0], before[0]) || after[1].Kind != runtime.TaskFailed || after[1].Data.HarnessOutcome != nil || after[1].Data.Text != "" || status.Result == nil || status.Result.Text != "" || status.Result.Usage != nil || calls != 1 || providerCalls.Load() != 0 {
		t.Fatal(status, after, err, calls, providerCalls.Load())
	}
}
