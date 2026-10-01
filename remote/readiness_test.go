package remote

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"path/filepath"
	"testing"
	"time"
)

func fixtureReadiness(i harness.Identity) harness.Readiness {
	return harness.Readiness{Identity: i, ExecutableMatched: true, CredentialState: "not_required", ModelState: "present", Compatible: true, Local: true, ContextTokens: 32768}
}
func (b rankingBackend) HarnessReadiness(context.Context, HarnessIdentityRequest, bool) (harness.Readiness, error) {
	return fixtureReadiness(b.identity), nil
}
func (b *automaticBackend) HarnessReadiness(context.Context, HarnessIdentityRequest, bool) (harness.Readiness, error) {
	return fixtureReadiness(b.identity), nil
}
func TestRemoteReadinessScopeAndIdentity(t *testing.T) {
	f := setup(t)
	task, _, _ := outcomeFixture(t)
	identity := *task.ExpectedHarnessIdentity
	f.serverPeer.Models = []string{task.ModelID}
	f.serverPeer.Harnesses = []string{task.HarnessID}
	writeRegistry(t, f.clientTrust, f.serverPeer)
	calls := 0
	b := &SDKBackend{Models: []Model{{ID: task.ModelID, Provider: identity.Provider, Model: identity.Model, Local: true, ContextTokens: 32768}}, Harnesses: []Harness{{ID: task.HarnessID, ModelID: task.ModelID, Kind: identity.Harness, ModelRevision: identity.ModelRevision}}, Identify: func(string, string, int) (harness.Identity, error) { return identity, nil }, CheckHarness: func(ctx context.Context, _ string, _ string, _ int) (harness.Readiness, error) {
		calls++
		d, ok := ctx.Deadline()
		if !ok || time.Until(d) > 2*time.Second {
			t.Error("unbounded observation")
		}
		return fixtureReadiness(identity), nil
	}}
	f.server.backend = b
	q := HarnessIdentityRequest{task.ModelID, task.HarnessID, 8192}
	if _, e := f.client.HarnessReadiness(context.Background(), "node-a", q); !errors.Is(e, ErrDenied) || calls != 0 {
		t.Fatal(e, calls)
	}
	f.clientPeer.Models = f.serverPeer.Models
	f.clientPeer.Harnesses = f.serverPeer.Harnesses
	writeRegistry(t, f.serverTrust, f.clientPeer)
	got, e := f.client.HarnessReadiness(context.Background(), "node-a", q)
	if e != nil || got.Readiness.Identity != identity || calls != 1 {
		t.Fatal(got, e, calls)
	}
	got.CheckedAt = got.CheckedAt.Add(-time.Minute)
	if got.valid() {
		t.Fatal("accepted stale observation")
	}
	b.Models[0].Local = false
	before := calls
	if _, e = b.HarnessReadiness(context.Background(), q, false); !errors.Is(e, ErrDenied) || calls != before {
		t.Fatal("cloud outside scope", e)
	}
	b.Models[0].Local = true
	b.CheckHarness = func(context.Context, string, string, int) (harness.Readiness, error) {
		r := fixtureReadiness(identity)
		r.Identity.ModelRevision = "changed"
		return r, nil
	}
	if _, e = f.client.HarnessReadiness(context.Background(), "node-a", q); e == nil {
		t.Fatal("accepted identity drift")
	}
	var n int
	if e = f.journal.db.QueryRow("SELECT count(*) FROM requests").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}

type unreadyBackend struct {
	rankingBackend
	observation harness.Readiness
}

func (b unreadyBackend) HarnessReadiness(context.Context, HarnessIdentityRequest, bool) (harness.Readiness, error) {
	return b.observation, nil
}
func TestRankingCannotOverrideNegativeReadiness(t *testing.T) {
	for _, mode := range []string{"artifact", "credentials", "model_absent", "model_unknown", "compatibility", "locality", "context", "cost", "capabilities"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _, task, v, _, b := reviewFixture(t)
			i := v.receipt.Execution.Actual
			r := fixtureReadiness(i)
			candidate := DestinationCandidate{Destination: "node-a", ModelID: task.ModelID, HarnessID: task.HarnessID, Candidate: harness.Candidate{Identity: i, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 32768, Capabilities: []string{"chat"}}}
			req := harness.Request{Version: 1, Task: v.receipt.Execution.Task, Mode: "local_only", LocalRequired: true, ContextTokens: 32768, Capabilities: []string{"chat"}}
			r.Capabilities = []string{"chat"}
			switch mode {
			case "artifact":
				r.ExecutableMatched = false
			case "credentials":
				r.CredentialState = "missing"
			case "model_absent":
				r.ModelState = "absent"
			case "model_unknown":
				r.ModelState = "unknown"
			case "compatibility":
				r.Compatible = false
			case "locality":
				r.Local = false
			case "context":
				r.ContextTokens = 8192
			case "cost":
				r.EstimatedCost = 1
			case "capabilities":
				r.Capabilities = nil
			}
			f.server.backend = unreadyBackend{rankingBackend{b, i}, r}
			got, e := f.client.RankRecordedCandidates(context.Background(), filepath.Join(t.TempDir(), "evidence"), req, harness.DefaultPolicy(), []DestinationCandidate{candidate}, 0)
			if !errors.Is(e, harness.ErrNoRoute) || len(got.Selection.Excluded) != 1 {
				t.Fatal("negative readiness bypassed", got, e)
			}
		})
	}
}
