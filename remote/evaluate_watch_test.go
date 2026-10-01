package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type watchBackend struct {
	*outcomeBackend
	polls      atomic.Int32
	state      string
	transition bool
}

func (b *watchBackend) Status(context.Context, string) (submissions.Status, error) {
	n := b.polls.Add(1)
	s := b.status
	if b.transition {
		if n == 1 {
			s.State = "queued"
		} else if n == 2 {
			s.State = "running"
		}
	} else {
		s.State = b.state
	}
	return s, nil
}

func TestWatchEvaluationWaitsThenReconcilesWithoutReplay(t *testing.T) {
	f, routes, key, task, v, _, b := reviewFixture(t)
	watched := &watchBackend{outcomeBackend: b, transition: true}
	f.server.backend = watched
	evaluator := &remoteEvaluatorFixture{}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	root := filepath.Join(t.TempDir(), "evidence")
	for range 2 {
		result, err := f.client.WatchRecordedEvaluation(context.Background(), routes, root, key, task, policy, 10*time.Second, time.Second)
		if err != nil || !result.ReviewApplied {
			t.Fatal(result, err)
		}
	}
	rank := remoteRank(t, root, v)
	if evaluator.calls.Load() != 1 || b.submits.Load() != 1 || rank.AdvisorySamples != 1 || rank.ConfirmedSamples != 0 || watched.polls.Load() < 4 {
		t.Fatal(evaluator.calls.Load(), b.submits.Load(), rank, watched.polls.Load())
	}
}

func TestWatchEvaluationNonSuccessNeverCreatesQualityEvidence(t *testing.T) {
	for _, mode := range []string{"failed", "canceled", "unknown", "deadline", "changed", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			f, routes, key, task, _, _, b := reviewFixture(t)
			state := mode
			if mode == "deadline" {
				state = "running"
			}
			watched := &watchBackend{outcomeBackend: b, state: state}
			f.server.backend = watched
			root := filepath.Join(t.TempDir(), "evidence")
			evaluator := &remoteEvaluatorFixture{}
			policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
			wait := time.Second
			if mode == "deadline" {
				wait = 50 * time.Millisecond
			}
			if mode == "changed" {
				task.Prompt = "different intent"
			}
			if mode == "revoked" {
				writeRegistry(t, f.clientTrust)
			}
			result, err := f.client.WatchRecordedEvaluation(context.Background(), routes, root, key, task, policy, wait, time.Second)
			if mode == "failed" || mode == "canceled" {
				if err != nil || result.Status != "task_"+mode {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("invalid or incomplete task reviewed", result)
			}
			if mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if evaluator.calls.Load() != 0 || b.submits.Load() != 1 {
				t.Fatal("unexpected work", evaluator.calls.Load(), b.submits.Load())
			}
			if _, err = os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("non-success wrote quality evidence", err)
			}
		})
	}
}
