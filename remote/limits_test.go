package remote

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestLimitsConcurrentWindowsAndPolicyChanges(t *testing.T) {
	p := Peer{ID: "peer", RequestLimits: &RequestLimits{Info: 1, Dispatch: 7, Inspect: 1, Cancel: 1}}
	reg := Registry{Peers: []Peer{p, {ID: "other"}}}
	var l requestLimiter
	now := time.Now()
	var admitted atomic.Int32
	var reports atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, report, _ := l.allow(reg, p, "dispatch", now)
			if ok {
				admitted.Add(1)
			}
			if report {
				reports.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 7 || reports.Load() != 1 {
		t.Fatal(admitted.Load(), reports.Load())
	}
	if ok, _, _ := l.allow(reg, p, "cancel", now); !ok {
		t.Fatal("dispatch exhausted cancellation")
	}
	if ok, _, _ := l.allow(reg, reg.Peers[1], "dispatch", now); !ok {
		t.Fatal("peer isolation")
	}
	p.RequestLimits.Dispatch = 8
	if ok, _, _ := l.allow(reg, p, "dispatch", now); !ok {
		t.Fatal("raised live policy")
	}
	p.RequestLimits.Dispatch = 1
	if ok, _, retry := l.allow(reg, p, "dispatch", now.Add(59*time.Second)); ok || retry != 1 {
		t.Fatal(ok, retry)
	}
	if ok, _, _ := l.allow(reg, p, "dispatch", now.Add(time.Minute)); !ok {
		t.Fatal("window did not expire")
	}
	l.allow(Registry{Peers: reg.Peers[1:]}, reg.Peers[1], "info", now)
	if _, ok := l.peers[p.ID]; ok {
		t.Fatal("revoked peer retained")
	}
}
func TestRemoteRateLimitsProtectDispatchAndPreserveCancellation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.clientPeer.RequestLimits = &RequestLimits{Info: 1, Dispatch: 1, Inspect: 1, Cancel: 2}
	writeRegistry(t, f.serverTrust, f.clientPeer)
	if _, err := f.client.Dispatch(ctx, "node-a", "limited-request-01", testTask()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := f.client.Dispatch(ctx, "node-a", "limited-request-02", testTask()); !errors.Is(err, ErrRateLimited) {
			t.Fatal(err)
		}
	}
	f.backend.mu.Lock()
	creates := f.backend.creates
	f.backend.mu.Unlock()
	if creates != 1 {
		t.Fatal("throttled request dispatched", creates)
	}
	var count int
	if err := f.journal.db.QueryRow("SELECT count(*) FROM audit WHERE outcome='rate_limited'").Scan(&count); err != nil || count != 1 {
		t.Fatal("throttle audit must be bounded", count, err)
	}
	if _, err := f.client.Cancel(ctx, "node-a", "limited-request-01"); err != nil {
		t.Fatal("cancel unavailable", err)
	}
	// A live registry update applies to the existing authenticated caller without
	// resetting its consumed allowance or changing durable request identity.
	f.clientPeer.RequestLimits.Dispatch = 2
	writeRegistry(t, f.serverTrust, f.clientPeer)
	if _, err := f.client.Dispatch(ctx, "node-a", "limited-request-01", testTask()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.Dispatch(ctx, "node-a", "limited-request-02", testTask()); !errors.Is(err, ErrRateLimited) {
		t.Fatal("update reset allowance", err)
	}
	if _, err := f.client.Info(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.Info(ctx, "node-a"); !errors.Is(err, ErrRateLimited) {
		t.Fatal(err)
	}
	writeRegistry(t, f.serverTrust)
	if _, err := f.client.Cancel(ctx, "node-a", "limited-request-01"); err == nil || errors.Is(err, ErrRateLimited) {
		t.Fatal("revocation did not fail authentication", err)
	}
}
func TestRequestLimitPolicyValidation(t *testing.T) {
	for _, n := range []int{-1, 0, 10001} {
		p := testPeer("node-a", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "https://127.0.0.1:443")
		p.RequestLimits = &RequestLimits{Info: n, Dispatch: 1, Inspect: 1, Cancel: 1}
		if p.Validate() == nil {
			t.Fatal("invalid limit accepted", n)
		}
	}
}
