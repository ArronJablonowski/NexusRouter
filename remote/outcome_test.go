package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type outcomeJournal struct{ events []runtime.Event }

func (j *outcomeJournal) Append(_ context.Context, seq int64, e runtime.Event) error {
	if seq != int64(len(j.events)) || e.Sequence != seq+1 || e.Validate() != nil {
		return ErrInvalid
	}
	copy, err := e.Clone()
	if err != nil {
		return err
	}
	j.events = append(j.events, copy)
	return nil
}
func outcomeFixture(t *testing.T) (Task, submissions.Status, []runtime.Event) {
	t.Helper()
	identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "provider", Model: "model", ModelRevision: "weights", ConfigSHA256: strings.Repeat("a", 64)}
	task := Task{Version: 1, ModelID: "configured", HarnessID: "fixture", HarnessDifficulty: "easy", Domain: "writing", Profile: "rubric-v1", Prompt: "precise input", ContextTokens: 8192, Private: true, ExpectedHarnessIdentity: &identity}
	j := &outcomeJournal{}
	_, text, err := runtime.RunHarness(context.Background(), j, runtime.HarnessRequest{TaskID: "task-id", SessionID: "session-id", SubmissionID: "submission-id", Messages: []providers.Message{{Role: "user", Content: task.Prompt}}, Privacy: "local_only", ContextTokens: 8192, MaxOutputBytes: 4096, Attribution: runtime.HarnessAttribution{Identity: identity, Task: harness.TaskClass{Domain: task.Domain, Profile: task.Profile, Difficulty: task.HarnessDifficulty}}, Execute: func(context.Context) (runtime.HarnessOutput, error) {
		return runtime.HarnessOutput{Actual: identity, Text: "answer"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return task, submissions.Status{Version: 1, ID: "submission-id", State: "succeeded", TaskIDs: []string{"task-id"}, Result: &submissions.Result{TaskID: "task-id", Text: text}}, j.events
}
func TestRecordedOutcomeBindsActualContentAndIntent(t *testing.T) {
	for _, mode := range []string{"valid", "model", "class", "output", "submission", "context", "prompt", "privacy", "partial"} {
		t.Run(mode, func(t *testing.T) {
			task, status, events := outcomeFixture(t)
			switch mode {
			case "model":
				task.ExpectedHarnessIdentity.Model = "other"
			case "class":
				task.HarnessDifficulty = "hard"
			case "output":
				status.Result.Text = "forged"
			case "submission":
				status.ID = "other"
			case "context":
				task.ContextTokens = 32768
			case "prompt":
				task.Prompt = "other"
			case "privacy":
				events[0].Data.Privacy = "cloud_allowed"
			case "partial":
				events = events[:1]
			}
			_, err := validateRecordedOutcome(task, status, events)
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
		})
	}
}
func TestOutcomePagesRejectChangingOrIncompleteHead(t *testing.T) {
	for _, mode := range []string{"valid", "head", "session", "gap", "running", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			_, _, events := outcomeFixture(t)
			read := func(after int64) (sessions.EventPage, error) {
				p := sessions.EventPage{Version: 1, TaskID: "task-id", SessionID: "session-id", State: "completed", FromSequence: after, NextSequence: after + 1, HeadSequence: 2, HasMore: after == 0, Events: events[after : after+1]}
				if after == 1 {
					switch mode {
					case "head":
						p.HeadSequence = 3
						p.HasMore = true
					case "session":
						p.SessionID = "other"
					case "gap":
						p.FromSequence = 0
					case "running":
						p.State = "running"
					}
				}
				if mode == "oversized" {
					p.HeadSequence = runtime.MaxHarnessAgentEvents + 1
					p.HasMore = true
				}
				return p, nil
			}
			got, err := readOutcomePages(read, "task-id")
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
			if err == nil && len(got) != 2 {
				t.Fatal(got)
			}
		})
	}
}
func TestOutcomeStorageIsolationConflictAndNoReview(t *testing.T) {
	task, status, events := outcomeFixture(t)
	execution, err := validateRecordedOutcome(task, status, events)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "evidence")
	v := VerifiedOutcome{verified: true, receipt: OutcomeReceipt{Version: 1, Route: RouteBinding{Version: 1, RequestID: "outcome-request-01", Destination: "node-a", CallerFingerprint: strings.Repeat("b", 64), TaskSHA256: hash(task)}, SubmissionID: status.ID, EventsSHA256: hash(events), Execution: execution}}
	ctx, now := context.Background(), time.Now().UTC()
	if err := (VerifiedOutcome{}).Record(ctx, root, now); err == nil {
		t.Fatal("unverified import")
	}
	for range 2 {
		if err = v.Record(ctx, root, now); err != nil {
			t.Fatal(err)
		}
	}
	changed := v
	changed.receipt.EventsSHA256 = strings.Repeat("c", 64)
	if err = changed.Record(ctx, root, now); err == nil {
		t.Fatal("provenance overwritten")
	}
	other := v
	other.receipt.Route.Destination = "node-b"
	if err = other.Record(ctx, root, now); err != nil {
		t.Fatal(err)
	}
	dirs, err := os.ReadDir(root)
	if err != nil || len(dirs) != 2 {
		t.Fatal(dirs, err)
	}
	for _, d := range dirs {
		ledger, err := harness.OpenEvidenceStore(filepath.Join(root, d.Name(), "ledger"))
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := ledger.Snapshot(ctx, now)
		ledger.Close()
		if err != nil {
			t.Fatal(err)
		}
		// A completed remote execution alone must not manufacture scored evidence.
		selection, err := harness.Select(harness.Request{Version: 1, Task: execution.Task, Mode: "local_only", LocalRequired: true, ContextTokens: 8192}, harness.DefaultPolicy(), []harness.Candidate{{Identity: execution.Actual, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192}}, snapshot, now, 0)
		if err != nil || len(selection.Ranked) != 1 || selection.Ranked[0].PendingOutputs != 1 || selection.Ranked[0].ConfirmedSamples != 0 || selection.Ranked[0].AdvisorySamples != 0 {
			t.Fatal("completion fabricated quality", selection, err)
		}

		receipts, err := filepath.Glob(filepath.Join(root, d.Name(), "*.outcome.json"))
		if err != nil || len(receipts) != 1 {
			t.Fatal(receipts, err)
		}
		body, err := os.ReadFile(receipts[0])
		if err != nil || strings.Contains(string(body), task.Prompt) || strings.Contains(string(body), "answer") {
			t.Fatal("raw content persisted", err)
		}
	}
}

func TestRecordedOutcomeRejectsChangedCallerBeforeRead(t *testing.T) {
	f := setup(t)
	task, _, _ := outcomeFixture(t)
	routes, err := OpenRouteStore(filepath.Join(t.TempDir(), "routes"))
	if err != nil {
		t.Fatal(err)
	}
	binding := RouteBinding{Version: 1, RequestID: "recorded-outcome-01", Destination: "node-a", CallerFingerprint: strings.Repeat("0", 64), TaskSHA256: hash(task)}
	if err = routes.Bind(binding); err != nil {
		t.Fatal(err)
	}
	if _, err = f.client.RecordedOutcome(context.Background(), routes, binding.RequestID, task); err != ErrConflict {
		t.Fatal("changed caller accepted", err)
	}
	task.ExpectedHarnessIdentity = nil
	if _, err = f.client.RecordedOutcome(context.Background(), routes, binding.RequestID, task); err != ErrInvalid {
		t.Fatal("missing identity pin accepted", err)
	}
}
