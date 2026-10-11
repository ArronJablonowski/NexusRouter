package remote

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"net/http"
	"testing"
)

func (b *fakeBackend) RecoverSubmission(ctx context.Context, key string, _ Task) (submissions.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if status, ok := b.tasks[key]; ok {
		return status, nil
	}
	return submissions.Status{}, sql.ErrNoRows
}

func TestRecoverStatusRepairsLostBindingWithoutDispatch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	task := testTask()
	key := "recover-lost-binding"
	f.backend.lost = true
	if _, err := f.client.Dispatch(ctx, "node-a", key, task); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	status, err := f.client.RecoverStatus(ctx, "node-a", key, task)
	if err != nil || status.ID == "" || f.backend.creates != 1 {
		t.Fatal(status, err, f.backend.creates)
	}
	if _, err = f.client.Status(ctx, "node-a", key); err != nil {
		t.Fatal(err)
	}
	changed := task
	changed.Prompt += " changed"
	if _, err = f.client.RecoverStatus(ctx, "node-a", key, changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if f.backend.creates != 1 {
		t.Fatal("recovery dispatched inference")
	}
}

func TestRecoverStatusMissingAdmissionDoesNotCreateWork(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	task := testTask()
	key := "recover-not-admitted"
	if _, err := f.journal.reserve(ctx, "node-b", key, hash(task)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.RecoverStatus(ctx, "node-a", key, task); !errors.Is(err, ErrNotAdmitted) {
		t.Fatal(err)
	}
	if f.backend.creates != 0 {
		t.Fatal("inspection created work")
	}
	// Simulate restart of the control server with its original durable journal.
	server, err := NewServer("node-a", TrustFile(f.serverTrust), f.journal, f.backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.recoverSubmission(ctx, "node-b", key, task); !errors.Is(err, ErrNotAdmitted) {
		t.Fatal(err)
	}
	if _, err = server.recoverSubmission(ctx, "foreign-caller", key, task); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestRecoverStatusOlderServerIsStillUncertain(t *testing.T) {
	f := setup(t)
	f.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Nexus-Instance", "node-a")
		w.WriteHeader(404)
	})
	if _, err := f.client.RecoverStatus(context.Background(), "node-a", "unknown-recovery-route", testTask()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
