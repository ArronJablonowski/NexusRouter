package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRouteBindingConcurrentChoiceAndReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	a, err := OpenRouteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenRouteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	original := RouteBinding{Version, "recorded-request-01", "node-a", strings.Repeat("a", 64), strings.Repeat("b", 64)}
	var wg sync.WaitGroup
	var chosen atomic.Int32
	var conflicts atomic.Int32
	for i := range 30 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := original
			candidate.Destination = "node-b"
			store := a
			if i%2 == 0 {
				candidate = original
				store = b
			}
			err := store.Bind(candidate)
			if err == nil {
				chosen.Add(1)
			} else if errors.Is(err, ErrConflict) {
				conflicts.Add(1)
			} else {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if chosen.Load() != 15 || conflicts.Load() != 15 {
		t.Fatal(chosen.Load(), conflicts.Load())
	}
	reopened, err := OpenRouteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.Lookup(original.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Bind(saved); err != nil {
		t.Fatal(err)
	}
	changed := saved
	changed.TaskSHA256 = strings.Repeat("c", 64)
	if err = reopened.Bind(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("task changed", err)
	}
	changed = saved
	changed.CallerFingerprint = strings.Repeat("d", 64)
	if err = reopened.Bind(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("caller changed", err)
	}
	raw, err := os.ReadFile(a.path(original.RequestID))
	if err != nil || strings.Contains(string(raw), "prompt") {
		t.Fatal(err)
	}
	// Corrupt/unsafe records fail closed; never overwrite them to regain a route.
	if err = os.WriteFile(a.path(original.RequestID), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = a.Bind(saved); err == nil {
		t.Fatal("corrupt binding replaced")
	}
}
func TestRecordedDispatchLostResponseCannotChangeDestination(t *testing.T) {
	f := setup(t)
	f.backend.lost = true
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "routes")
	store, err := OpenRouteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := "recorded-lost-0001"
	task := testTask()
	if _, err = f.client.DispatchRecorded(ctx, store, "node-a", key, task); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	saved, err := store.Lookup(key)
	if err != nil || saved.Destination != "node-a" || saved.TaskSHA256 != hash(task) {
		t.Fatal(saved, err)
	}
	second := f.serverPeer
	second.ID = "node-c"
	second.Pins = []string{strings.Repeat("e", 64)}
	writeRegistry(t, f.clientTrust, f.serverPeer, second)
	if _, err = f.client.DispatchRecorded(ctx, store, "node-c", key, task); !errors.Is(err, ErrConflict) {
		t.Fatal("changed destination after loss", err)
	}
	restarted, err := OpenRouteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.client.DispatchRecorded(ctx, restarted, "node-a", key, task)
	if err != nil || result.State != "queued" || f.backend.creates != 1 {
		t.Fatal(result, err, f.backend.creates)
	}
	task.Prompt = "changed"
	if _, err = f.client.DispatchRecorded(ctx, restarted, "node-a", key, task); !errors.Is(err, ErrConflict) {
		t.Fatal("changed payload", err)
	}
}
func TestRouteStoreRejectsUnsafeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	s, err := OpenRouteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := RouteBinding{Version, "recorded-request-01", "node-a", strings.Repeat("a", 64), strings.Repeat("b", 64)}
	target := filepath.Join(t.TempDir(), "target")
	if err = os.WriteFile(target, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, s.path(b.RequestID)); err != nil {
		t.Fatal(err)
	}
	if err = s.Bind(b); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "sentinel" {
		t.Fatal("symlink target changed")
	}
}
