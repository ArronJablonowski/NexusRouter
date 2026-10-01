package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

type rankingBackend struct {
	*outcomeBackend
	identity harness.Identity
}

func (b rankingBackend) HarnessIdentity(context.Context, HarnessIdentityRequest, bool) (harness.Identity, error) {
	return b.identity, nil
}

func TestRemoteRankingUsesBoundEvidenceAndFreshIdentity(t *testing.T) {
	t.Run("https", func(t *testing.T) { testRemoteRanking(t, false) })
	t.Run("ssh", func(t *testing.T) {
		if os.Getenv("NEXUS_REMOTE_SSH_NATIVE") != "1" {
			t.Skip("native SSH opt-in")
		}
		testRemoteRanking(t, true)
	})
}
func testRemoteRanking(t *testing.T, ssh bool) {
	f, routes, key, task, v, review, b := reviewFixture(t)
	f.server.backend = rankingBackend{b, v.receipt.Execution.Actual}
	if ssh {
		transport := nativeSSHServer(t)
		f.serverPeer.Transport = "ssh"
		f.serverPeer.SSH = &transport
		writeRegistry(t, f.clientTrust, f.serverPeer)
	}

	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "evidence")
	if err := f.client.ReviewRecordedOutcome(ctx, routes, root, key, task, review); err != nil {
		t.Fatal(err)
	}
	candidate := DestinationCandidate{Destination: "node-a", ModelID: task.ModelID, HarnessID: task.HarnessID, Candidate: harness.Candidate{Identity: v.receipt.Execution.Actual, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192}}
	req := harness.Request{Version: 1, Task: v.receipt.Execution.Task, Mode: "local_only", LocalRequired: true, ContextTokens: 8192}
	got, err := f.client.RankRecordedCandidates(ctx, root, req, harness.DefaultPolicy(), []DestinationCandidate{candidate}, 0)
	if err != nil || got.ModelID != task.ModelID || got.HarnessID != task.HarnessID || got.Selection.Primary.Ranked.AdvisorySamples != 1 || got.Selection.Primary.Ranked.ConfirmedSamples != 0 || got.CallerFingerprint != v.receipt.Route.CallerFingerprint {
		t.Fatal(got, err)
	}
	f.server.backend = waitingRankingBackend{rankingBackend{b, v.receipt.Execution.Actual}}
	denied, capacityErr := f.client.RankRecordedCandidates(ctx, root, req, harness.DefaultPolicy(), []DestinationCandidate{candidate}, 0)
	if !errors.Is(capacityErr, harness.ErrNoRoute) || len(denied.Selection.Excluded) != 1 {
		t.Fatal("caller flag bypassed measured capacity", denied, capacityErr)
	}
	f.server.backend = rankingBackend{b, v.receipt.Execution.Actual}
	// A new task difficulty cannot borrow reviewed samples from another class.
	req.Task.Difficulty = "hard"
	got, err = f.client.RankRecordedCandidates(ctx, root, req, harness.DefaultPolicy(), []DestinationCandidate{candidate}, 0)
	if err != nil || got.Selection.Primary.Ranked.AdvisorySamples != 0 {
		t.Fatal(got, err)
	}
	req.Task = v.receipt.Execution.Task
	candidate.Candidate.Identity.ConfigSHA256 = hash("changed-context")
	got, err = f.client.RankRecordedCandidates(ctx, root, req, harness.DefaultPolicy(), []DestinationCandidate{candidate}, 0)
	if !errors.Is(err, harness.ErrNoRoute) || len(got.Selection.Excluded) != 1 {
		t.Fatal("stale identity selected", got, err)
	}
	candidate.Candidate.Identity = v.receipt.Execution.Actual
	f.serverPeer.Operations = []string{"info"}
	writeRegistry(t, f.clientTrust, f.serverPeer)
	got, err = f.client.RankRecordedCandidates(ctx, root, req, harness.DefaultPolicy(), []DestinationCandidate{candidate}, 0)
	if !errors.Is(err, harness.ErrNoRoute) {
		t.Fatal("unauthorized selected", got, err)
	}
	if b.submits.Load() != 1 {
		t.Fatal("ranking submitted inference")
	}
}
func TestDestinationEvidenceMissingUnknownCorruptFailsClosed(t *testing.T) {
	_, _, _, _, v, _, _ := reviewFixture(t)
	root := filepath.Join(t.TempDir(), "evidence")
	now := time.Now().UTC()
	ctx := context.Background()
	if _, err := readDestinationEvidence(ctx, root, v.receipt.Route.Destination, v.receipt.Route.CallerFingerprint, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("ranking created evidence", err)
	}
	if err := v.Record(ctx, root, now); err != nil {
		t.Fatal(err)
	}
	// Another caller's evidence is not read even at the same destination.
	empty, err := readDestinationEvidence(ctx, root, v.receipt.Route.Destination, hash("other-caller"), now)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := harness.Select(harness.Request{Version: 1, Task: v.receipt.Execution.Task, Mode: "local_only", ContextTokens: 8192}, harness.DefaultPolicy(), []harness.Candidate{{Identity: v.receipt.Execution.Actual, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192}}, empty, now, 0)
	if err != nil || selection.Primary.PendingOutputs != 0 {
		t.Fatal(selection, err)
	}
	scope := hash(struct{ Destination, Caller string }{v.receipt.Route.Destination, v.receipt.Route.CallerFingerprint})
	path := filepath.Join(root, scope, "ledger", "harness-evidence.sqlite")
	if err := os.WriteFile(path, []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDestinationEvidence(ctx, root, v.receipt.Route.Destination, v.receipt.Route.CallerFingerprint, now); err == nil {
		t.Fatal("corrupt ledger treated as unknown")
	}
}
