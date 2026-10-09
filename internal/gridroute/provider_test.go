package gridroute

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type executorFixture struct {
	candidate                   app.FederatedCandidate
	task                        remote.Task
	dispatches, cancels, checks int
	reject, lost, wait          bool
	unresolved                  bool
}

func (f *executorFixture) RankRecordedCandidates(_ context.Context, _ string, _ harness.Request, _ harness.Policy, _ []remote.DestinationCandidate, _ float64) (remote.DestinationSelection, error) {
	f.checks++
	if f.reject {
		return remote.DestinationSelection{}, remote.ErrDenied
	}
	return remote.DestinationSelection{CallerFingerprint: f.candidate.CallerFingerprint, Selection: harness.ScopedSelection{Primary: harness.ScopedRanked{Scope: f.candidate.Instance, Ranked: harness.Ranked{Identity: f.candidate.Identity}}}}, nil
}
func (f *executorFixture) DispatchRecordedAs(_ context.Context, store *remote.RouteStore, host, key string, task remote.Task, caller string) (submissions.Status, error) {
	f.dispatches++
	f.task = task
	if err := store.Bind(remote.RouteBinding{Version: 1, RequestID: key, Destination: host, CallerFingerprint: caller, TaskSHA256: strings.Repeat("b", 64)}); err != nil {
		return submissions.Status{}, err
	}
	if f.lost {
		return submissions.Status{}, remote.ErrUnavailable
	}
	if f.wait {
		return submissions.Status{State: "running"}, nil
	}
	return submissions.Status{State: "succeeded", Result: &submissions.Result{TaskID: "remote-task", Text: "answer", FinishReason: "stop"}}, nil
}
func (f *executorFixture) InspectRecorded(context.Context, *remote.RouteStore, string) (remote.RecordedRequestStatus, error) {
	return remote.RecordedRequestStatus{Destination: f.candidate.Instance, Status: submissions.Status{State: "running"}}, nil
}
func (f *executorFixture) CancelRecorded(context.Context, *remote.RouteStore, string) (submissions.Status, error) {
	f.cancels++
	if f.unresolved {
		return submissions.Status{}, remote.ErrUnavailable
	}
	return submissions.Status{State: "canceled"}, nil
}
func TestFederatedProviderBindsConversationAndNeverRedispatches(t *testing.T) {
	for _, scenario := range []string{"success", "revoked", "lost", "unresolved", "canceled", "sink_failure"} {
		t.Run(scenario, func(t *testing.T) {
			identity := harness.Identity{Version: 1, Harness: "pi", HarnessVersion: "1", AdapterVersion: "1", Provider: "ollama", Model: "coder", ModelRevision: "weights-1", ConfigSHA256: strings.Repeat("c", 64)}
			c := app.FederatedCandidate{CallerFingerprint: strings.Repeat("a", 64), Instance: "spark", DestinationModel: "coder", HarnessID: "pi-coder", Identity: identity, Candidate: routing.Candidate{Local: true, ContextTokens: 32768, Capabilities: []string{"chat"}}}
			c.Model.Model = "coder"
			f := &executorFixture{candidate: c, reject: scenario == "revoked", lost: scenario == "lost" || scenario == "unresolved", wait: scenario == "canceled", unresolved: scenario == "unresolved"}
			store, err := remote.OpenRouteStore(filepath.Join(t.TempDir(), "routes"))
			if err != nil {
				t.Fatal(err)
			}
			b := &Bridge{Store: store, executor: f}
			adapter, err := b.Open(context.Background(), c, "parent-task-identity", routing.Request{Mode: "local_only", Domain: "code", Profile: "default", ContextTokens: 8192})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "canceled" {
				timer := time.AfterFunc(30*time.Millisecond, cancel)
				defer timer.Stop()
			}
			input := providers.Request{Model: "coder", Messages: []providers.Message{{Role: "user", Content: "prior"}, {Role: "assistant", Content: "prior answer"}, {Role: "user", Content: "next"}}}
			chunks := 0
			err = adapter.Stream(ctx, input, func(providers.Chunk) error {
				chunks++
				if scenario == "sink_failure" {
					return errors.New("sink failed")
				}
				return nil
			})
			if scenario == "success" {
				if err != nil || chunks != 2 || f.dispatches != 1 || f.cancels != 0 || len(f.task.Messages) != 3 || !f.task.Private || f.task.Execution.Deadline.IsZero() {
					t.Fatal(err, chunks, f)
				}
			} else if err == nil {
				t.Fatal("failure hidden")
			}
			var failure *providers.Failure
			if scenario == "lost" || scenario == "revoked" {
				if !errors.As(err, &failure) || !failure.Retryable {
					t.Fatal("confirmed no-output failure cannot retry", err)
				}
			} else if scenario != "success" && errors.As(err, &failure) && failure.Retryable {
				t.Fatal("unresolved, canceled or delivered attempt became retryable", err)
			}
			if scenario == "revoked" && f.dispatches != 0 {
				t.Fatal("revoked destination dispatched")
			}
			if scenario == "lost" || scenario == "unresolved" || scenario == "canceled" || scenario == "sink_failure" {
				if f.cancels != 1 {
					t.Fatal("uncertain remote work not canceled", f.cancels)
				}
			}
			if adapter.Stream(context.Background(), input, func(providers.Chunk) error { return nil }) == nil || f.dispatches > 1 {
				t.Fatal("reused adapter redispatched")
			}
			if f.dispatches > 0 {

				binding, err := store.Lookup("federated-parent-task-identity")
				if err != nil || binding.Destination != "spark" {
					t.Fatal(binding, err)
				}
			}
		})
	}
}
