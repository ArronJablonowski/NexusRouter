package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func leaseStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "leases.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Append(context.Background(), 0, event("task", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestReaderWriterLeasesAndExpiry(t *testing.T) {
	s, _ := leaseStore(t)
	ctx := context.Background()
	now := time.Unix(100, 0)
	a, err := s.AcquireLease(ctx, "task", "a", "resource", false, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AcquireLease(ctx, "task", "b", "resource", false, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireLease(ctx, "task", "c", "resource", true, now, time.Second); !errors.Is(err, ErrLeaseBusy) {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, a.Token, "wrong"); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("wrong owner released", err)
	}
	if err := s.ReleaseLease(ctx, a.Token, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, b.Token, "b"); err != nil {
		t.Fatal(err)
	}
	w, err := s.AcquireLease(ctx, "task", "c", "resource", true, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Second)
	for _, writer := range []bool{true, false} {
		if _, err := s.AcquireLease(ctx, "task", "d", "resource", writer, later, time.Second); !errors.Is(err, ErrLeaseBusy) {
			t.Fatal("expired writer allowed overlap", err)
		}
	}
	if err := s.RenewLease(ctx, w.Token, "c", later, time.Second); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("expired lease revived", err)
	}
	if err := s.ReleaseLease(ctx, w.Token, "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireLease(ctx, "task", "d", "resource", true, later, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseHeartbeatAndRestart(t *testing.T) {
	s, path := leaseStore(t)
	ctx := context.Background()
	now := time.Unix(100, 0)
	l, err := s.AcquireLease(ctx, "task", "worker", "scope", true, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenewLease(ctx, l.Token, "worker", now.Add(time.Millisecond), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	leases, err := reopened.InspectLeases(ctx, "scope")
	if err != nil || len(leases) != 1 || leases[0].Token != l.Token || !leases[0].Expires.After(l.Expires) {
		t.Fatalf("%+v %v", leases, err)
	}
	if _, err := reopened.AcquireLease(ctx, "task", "other", "scope", true, now.Add(time.Hour), time.Second); !errors.Is(err, ErrLeaseBusy) {
		t.Fatal("restart cleared uncertain writer", err)
	}
}

func TestConcurrentWritersOneWinner(t *testing.T) {
	s, path := leaseStore(t)
	other, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, db := range []*Store{s, other} {
		wg.Add(1)
		go func(db *Store) {
			defer wg.Done()
			_, err := db.AcquireLease(context.Background(), "task", "worker", "scope", true, time.Unix(100, 0), time.Second)
			results <- err
		}(db)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrLeaseBusy) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("writer winners", wins)
	}
}
