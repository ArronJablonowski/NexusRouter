package workers_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

func workerAuthorityDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func workerCompactionAuthority(t *testing.T, root string) *runtime.DelegationCompactionAuthority {
	t.Helper()
	authority, err := runtime.SealDelegationCompactionAuthority(runtime.DelegationCompactionAuthority{
		RootTaskID: root, PlanDigest: workerAuthorityDigest("plan"), InheritedEngineDigest: workerAuthorityDigest("engine"),
		Scope: "delegation-" + root, ParentPolicy: json.RawMessage(`{"read":"allow","write":"ask"}`),
		ChildPolicy: json.RawMessage(`{"read":"allow","write":"deny"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &authority
}

func authorityWork(t *testing.T, task string) workers.Work {
	t.Helper()
	w := work(task)
	w.ParentID, w.Scope, w.WorkerID = "root", "delegation-root", "worker"
	w.DelegationOrigin = &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}
	w.DelegationCompaction = workerCompactionAuthority(t, w.ParentID)
	return w
}

func TestWorkerPersistsDelegationCompactionAuthorityOnlyAtStart(t *testing.T) {
	db, sup := setup(t)
	w := authorityWork(t, "authority-work")
	if output, err := sup.Run(context.Background(), w); err != nil || output != "answer" {
		t.Fatal(output, err)
	}
	events, err := db.Read(context.Background(), w.TaskID, 0, 100)
	if err != nil || len(events) == 0 {
		t.Fatal(events, err)
	}
	for index, event := range events {
		if index == 0 {
			if event.Kind != runtime.TaskStarted || event.Data.DelegationCompaction == nil ||
				event.Data.DelegationCompaction.Validate() != nil || event.Data.DelegationCompaction.AuthorityDigest != w.DelegationCompaction.AuthorityDigest {
				t.Fatal("worker start lost authority", event)
			}
			continue
		}
		if event.Data.DelegationCompaction != nil {
			t.Fatal("authority escaped worker start", event.Kind)
		}
	}
}

func TestWorkerRejectsDelegationCompactionAuthorityBeforeDurableBoundary(t *testing.T) {
	for name, mutate := range map[string]func(*workers.Work){
		"invalid authority": func(w *workers.Work) { w.DelegationCompaction.PlanDigest = workerAuthorityDigest("changed") },
		"wrong root":        func(w *workers.Work) { w.ParentID = "other-root" },
		"wrong scope":       func(w *workers.Work) { w.Scope = "delegation-other" },
		"root work alias":   func(w *workers.Work) { w.TaskID = w.ParentID },
		"missing origin":    func(w *workers.Work) { w.DelegationOrigin = nil },
		"invalid origin":    func(w *workers.Work) { w.DelegationOrigin.ToolName = "shell" },
	} {
		t.Run(name, func(t *testing.T) {
			supervisor, adapters := newSlotSupervisor(t, 1)
			w := authorityWork(t, "rejected-work")
			var executed atomic.Bool
			w.Execute = func(context.Context) (string, error) { executed.Store(true); return "unsafe", nil }
			mutate(&w)
			if output, err := supervisor.Run(context.Background(), w); err != workers.ErrWork || output != "" || executed.Load() {
				t.Fatal("invalid authority crossed admission", output, err)
			}
			assertSlotAdaptersUntouched(t, adapters)
		})
	}
}

func TestWorkerDelegationCompactionAuthorityOwnsCallerPolicies(t *testing.T) {
	db, _ := setup(t)
	w := authorityWork(t, "owned-authority")
	caller := w.DelegationCompaction
	journal := originJournal(func(ctx context.Context, sequence int64, event runtime.Event) error {
		if event.Kind == runtime.TaskStarted {
			caller.ParentPolicy[0], caller.ChildPolicy[0] = 'x', 'x'
			caller.Scope = "delegation-mutated"
			if event.Data.DelegationCompaction == nil || event.Data.DelegationCompaction.Validate() != nil ||
				event.Data.DelegationCompaction.Scope != "delegation-root" || event.Data.DelegationCompaction.ParentPolicy[0] != '{' {
				t.Error("worker start borrowed caller authority")
			}
		}
		return db.Append(ctx, sequence, event)
	})
	supervisor, err := workers.New(1, time.Millisecond, time.Second, db, journal)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := supervisor.Run(context.Background(), w); err != nil || output != "answer" {
		t.Fatal(output, err)
	}
	events, err := db.Read(context.Background(), w.TaskID, 0, 2)
	if err != nil || len(events) < 1 || events[0].Data.DelegationCompaction == nil || events[0].Data.DelegationCompaction.Validate() != nil {
		t.Fatal("owned authority did not persist", events, err)
	}
}

func TestWorkerLegacyWorkOmitsDelegationCompactionAuthority(t *testing.T) {
	db, sup := setup(t)
	w := work("legacy-authority-free")
	if output, err := sup.Run(context.Background(), w); err != nil || output != "answer" {
		t.Fatal(output, err)
	}
	events, err := db.Read(context.Background(), w.TaskID, 0, 2)
	if err != nil || len(events) == 0 || events[0].Data.DelegationCompaction != nil {
		t.Fatal("legacy work gained authority", events, err)
	}
}
