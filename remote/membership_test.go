package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPeerMembershipPreservesScopesAndRejectsConflicts(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	trust := TrustFile(filepath.Join(dir, "peers.json"))
	a := testPeer("node-a", strings.Repeat("a", 64), "https://127.0.0.1:443")
	b := testPeer("node-b", strings.Repeat("b", 64), "https://127.0.0.2:443")
	b.Operations = []string{"info"}
	first, err := trust.Pair(a, "absent")
	if err != nil {
		t.Fatal(err)
	}
	second, err := trust.Pair(b, first.Digest())
	if err != nil || !reflect.DeepEqual(second.Peers, []Peer{a, b}) {
		t.Fatal(second, err)
	}
	if _, err = trust.Pair(a, second.Digest()); !errors.Is(err, ErrConflict) {
		t.Fatal("replaced existing peer", err)
	}
	if _, err = trust.Revoke(a.ID, first.Digest()); !errors.Is(err, ErrConflict) {
		t.Fatal("stale removal", err)
	}
	collision := b
	collision.ID = "node-c"
	if _, err = trust.Pair(collision, second.Digest()); err == nil {
		t.Fatal("reused another member's pin")
	}
	current, err := trust.Read()
	if err != nil || current.Digest() != second.Digest() {
		t.Fatal("failed updates changed registry", current, err)
	}
	remaining, err := trust.Revoke(a.ID, second.Digest())
	if err != nil || !reflect.DeepEqual(remaining.Peers, []Peer{b}) {
		t.Fatal(remaining, err)
	}
	if _, err = trust.Revoke(a.ID, remaining.Digest()); !errors.Is(err, ErrConflict) {
		t.Fatal("missing peer removal", err)
	}
	empty, err := trust.Revoke(b.ID, remaining.Digest())
	if err != nil || len(empty.Peers) != 0 {
		t.Fatal(empty, err)
	}
}

func TestPeerMembershipConcurrentChangesDoNotLoseMembers(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	trust := TrustFile(filepath.Join(dir, "peers.json"))
	first, err := trust.Pair(testPeer("original", strings.Repeat("a", 64), "https://127.0.0.1:443"), "absent")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, name := range []string{"node-b", "node-c"} {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			_, err := trust.Pair(testPeer(name, strings.Repeat(string(rune('b'+i)), 64), "https://127.0.0.1:443"), first.Digest())
			results <- err
		}(i, name)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	current, err := trust.Read()
	if err != nil || successes != 1 || len(current.Peers) != 2 || current.Peers[0].ID != "original" {
		t.Fatal(current, successes, err)
	}
}

func TestMembershipRevocationBlocksNextAuthenticatedRequest(t *testing.T) {
	f := setup(t)
	if _, err := f.client.Info(context.Background(), "node-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(f.serverTrust), 0700); err != nil {
		t.Fatal(err)
	}
	trust := TrustFile(f.serverTrust)
	current, err := trust.Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = trust.Revoke(f.clientPeer.ID, current.Digest()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.client.Info(context.Background(), "node-a"); err == nil {
		t.Fatal("revoked peer retained authority")
	}
}
