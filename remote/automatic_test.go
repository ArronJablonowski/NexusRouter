package remote

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

type automaticBackend struct {
	fakeBackend
	identity harness.Identity
}

func (b *automaticBackend) HarnessIdentity(context.Context, HarnessIdentityRequest, bool) (harness.Identity, error) {
	return b.identity, nil
}
func automaticFixture(t *testing.T) (*fixture, *RouteStore, AutomaticRequest, []DestinationCandidate, *automaticBackend, *automaticBackend) {
	t.Helper()
	f := setup(t)
	task, _, _ := outcomeFixture(t)
	a := &automaticBackend{identity: *task.ExpectedHarnessIdentity}
	f.server.backend = a
	f.clientPeer.Models = []string{task.ModelID}
	f.clientPeer.Harnesses = []string{task.HarnessID}
	f.serverPeer.Models = []string{task.ModelID}
	f.serverPeer.Harnesses = []string{task.HarnessID}
	writeRegistry(t, f.serverTrust, f.clientPeer)
	cred, pin := f.ca.leaf(t, "node-c")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	trust := filepath.Join(dir, "trust.json")
	writeRegistry(t, trust, f.clientPeer)
	journal, err := OpenJournal(filepath.Join(dir, "journal"), "node-c")
	if err != nil {
		t.Fatal(err)
	}
	b := &automaticBackend{identity: *task.ExpectedHarnessIdentity}
	server, err := NewServer("node-c", TrustFile(trust), journal, b)
	if err != nil {
		t.Fatal(err)
	}
	http, err := server.HTTPServer(ln.Addr().String(), cred)
	if err != nil {
		t.Fatal(err)
	}
	go http.ServeTLS(ln, "", "")
	t.Cleanup(func() { http.Close(); journal.Close() })
	peer := testPeer("node-c", pin, "https://"+ln.Addr().String())
	peer.Models = f.serverPeer.Models
	peer.Harnesses = f.serverPeer.Harnesses
	writeRegistry(t, f.clientTrust, f.serverPeer, peer)
	routes, err := OpenRouteStore(filepath.Join(t.TempDir(), "routes"))
	if err != nil {
		t.Fatal(err)
	}
	request := AutomaticRequest{Version: 1, Prompt: task.Prompt, Routing: harness.Request{Version: 1, Task: harness.TaskClass{Domain: task.Domain, Profile: task.Profile, Difficulty: task.HarnessDifficulty}, Mode: "local_only", LocalRequired: true, ContextTokens: 8192}}
	c := harness.Candidate{Identity: *task.ExpectedHarnessIdentity, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192}
	return f, routes, request, []DestinationCandidate{{Destination: "node-a", ModelID: task.ModelID, HarnessID: task.HarnessID, Candidate: c}, {Destination: "node-c", ModelID: task.ModelID, HarnessID: task.HarnessID, Candidate: c}}, a, b
}
func TestAutomaticLostResponseRecoversOriginalDestination(t *testing.T) {
	f, routes, request, candidates, a, b := automaticFixture(t)
	a.lost = true
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "evidence")
	key := "automatic-lost-01"
	_, choice, err := f.client.DispatchAutomatic(ctx, routes, root, key, request, harness.DefaultPolicy(), candidates[:1], 0)
	if !errors.Is(err, ErrUnavailable) || choice.Destination != "node-a" {
		t.Fatal(choice, err)
	}
	reopened, err := OpenRouteStore(routes.directory)
	if err != nil {
		t.Fatal(err)
	}
	// Missing original candidate, invalid current policy and a newly preferred
	// alternate must never cause a second choice for this saved request.
	status, recovered, err := f.client.DispatchAutomatic(ctx, reopened, root, key, request, harness.Policy{}, candidates[1:], .9)
	if err != nil || status.State != "queued" || recovered != choice {
		t.Fatal(status, recovered, err)
	}
	if a.creates != 1 || b.creates != 0 {
		t.Fatal("duplicate or alternate inference", a.creates, b.creates)
	}
	bound, err := routes.Lookup(key)
	if err != nil {
		t.Fatal(err)
	}
	task, err := choice.Task(request)
	if err != nil || bound.TaskSHA256 != hash(task) {
		t.Fatal(bound, task, err)
	}
	body, err := os.ReadFile(routes.choicePath(key))
	if err != nil || strings.Contains(string(body), request.Prompt) {
		t.Fatal("prompt stored", err)
	}
	request.Prompt += " changed"
	if _, _, err = f.client.DispatchAutomatic(ctx, routes, root, key, request, harness.DefaultPolicy(), candidates, 0); !errors.Is(err, ErrConflict) {
		t.Fatal("changed intent accepted", err)
	}
}
func TestAutomaticConcurrentDifferentChoicesOnlyDispatchOneNode(t *testing.T) {
	f, routes, request, candidates, a, b := automaticFixture(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "evidence")
	key := "automatic-race-01"
	other, err := OpenRouteStore(routes.directory)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	choices := make(chan AutomaticChoice, 2)
	errs := make(chan error, 2)
	for i, store := range []*RouteStore{routes, other} {
		wg.Add(1)
		go func(i int, store *RouteStore) {
			defer wg.Done()
			_, choice, err := f.client.DispatchAutomatic(ctx, store, root, key, request, harness.DefaultPolicy(), candidates[i:i+1], 0)
			choices <- choice
			errs <- err
		}(i, store)
	}
	wg.Wait()
	close(errs)
	close(choices)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := <-choices
	second := <-choices
	if first != second {
		t.Fatal("two saved winners", first, second)
	}
	if a.creates+b.creates != 1 {
		t.Fatal("two destinations dispatched", a.creates, b.creates)
	}
}
func TestAutomaticUnsafeOrManualBindingNeverReranks(t *testing.T) {
	for _, mode := range []string{"corrupt", "symlink", "manual", "credential"} {
		t.Run(mode, func(t *testing.T) {
			f, routes, request, candidates, a, b := automaticFixture(t)
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "evidence")
			key := "automatic-deny-01"
			switch mode {
			case "corrupt":
				if err := os.WriteFile(routes.choicePath(key), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, routes.choicePath(key)); err != nil {
					t.Fatal(err)
				}
			case "manual":
				if err := routes.Bind(RouteBinding{Version: 1, RequestID: key, Destination: "node-c", CallerFingerprint: hash("caller"), TaskSHA256: hash("manual")}); err != nil {
					t.Fatal(err)
				}
			case "credential":
				_, choice, err := f.client.DispatchAutomatic(ctx, routes, root, key, request, harness.DefaultPolicy(), candidates[:1], 0)
				if err != nil {
					t.Fatal(err)
				}
				if choice.Destination != "node-a" {
					t.Fatal(choice)
				}
				creds, _ := f.ca.leaf(t, "new-caller")
				f.client.Credentials = creds
			}
			if _, _, err := f.client.DispatchAutomatic(ctx, routes, root, key, request, harness.DefaultPolicy(), candidates, 0); err == nil {
				t.Fatal("unsafe request dispatched")
			}
			expected := 0
			if mode == "credential" {
				expected = 1
			}
			if a.creates+b.creates != expected {
				t.Fatal("unexpected dispatch", a.creates, b.creates)
			}
		})
	}
}

func TestAutomaticRecoversChoiceBeforeRouteBinding(t *testing.T) {
	f, routes, request, candidates, a, b := automaticFixture(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "evidence")
	key := "automatic-crash-01"
	first, choice, err := f.client.DispatchAutomatic(ctx, routes, root, key, request, harness.DefaultPolicy(), candidates[:1], 0)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the on-disk state after the choice was synced but before the
	// route binding was published. Never remove the original committed files.
	recovered, err := OpenRouteStore(filepath.Join(t.TempDir(), "recovered"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(routes.choicePath(key))
	if err != nil {
		t.Fatal(err)
	}
	if err = immutableReceipt(recovered, recovered.choicePath(key), raw); err != nil {
		t.Fatal(err)
	}
	second, replayed, err := f.client.DispatchAutomatic(ctx, recovered, "", key, request, harness.Policy{}, nil, 0)
	if err != nil || second.ID != first.ID || replayed != choice || a.creates != 1 || b.creates != 0 {
		t.Fatal(first, second, replayed, err)
	}
	if _, err = recovered.Lookup(key); err != nil {
		t.Fatal("route binding not repaired", err)
	}
}
