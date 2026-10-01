package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

// DestinationCandidate is supplied by a trusted embedding host after capability,
// resource, locality and credential checks. Discovery's available RAM alone is
// not CapacityAvailable. No caller/remote-advertised quality score is accepted.
type DestinationCandidate struct {
	Destination string
	ModelID     string
	HarnessID   string
	Candidate   harness.Candidate
}
type DestinationSelection struct {
	Version           int
	CallerFingerprint string
	ModelID           string
	HarnessID         string
	Selection         harness.ScopedSelection
}

// RankRecordedCandidates reads only caller-owned destination evidence and fresh
// configured identities. It does not dispatch, reserve resources or choose a new
// destination for an existing request. The host must recheck admission and use
// DispatchRecorded to persist the selected choice before sending any task.
func (c *Client) RankRecordedCandidates(ctx context.Context, root string, request harness.Request, policy harness.Policy, candidates []DestinationCandidate, draw float64) (DestinationSelection, error) {
	var out DestinationSelection
	if c == nil || ctx == nil || ctx.Err() != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root || len(candidates) > 4096 || request.ContextTokens < 8192 || request.ContextTokens > 1<<24 {
		return out, ErrInvalid
	}
	if _, err := harness.SelectScoped(request, policy, nil, time.Now().UTC(), draw); err != nil && !errors.Is(err, harness.ErrNoRoute) {
		return out, err
	}
	cert, _, err := c.Credentials.load()
	if err != nil || len(cert.Certificate) == 0 {
		return out, ErrDenied
	}
	pin := certificateDigest(cert.Certificate[0])
	out.Version = Version
	out.CallerFingerprint = pin
	registry, err := c.Trust.Read()
	if err != nil {
		return out, err
	}
	scoped := make([]harness.ScopedCandidate, 0, len(candidates))
	// Copy candidates so policy filtering never changes caller-owned input.
	for _, proposal := range candidates {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if !id(proposal.Destination) || !name(proposal.ModelID) || !name(proposal.HarnessID) || proposal.HarnessID == "auto" {
			return out, ErrInvalid
		}
		candidate := proposal.Candidate
		peer, e := registry.peer(proposal.Destination)
		intent := Task{Version: 1, ModelID: proposal.ModelID, HarnessID: proposal.HarnessID, HarnessDifficulty: request.Task.Difficulty, Domain: request.Task.Domain, Profile: request.Task.Profile, ContextTokens: int(request.ContextTokens), MaxCost: request.MaxCost, Private: request.LocalRequired, Prompt: "ranking-only", ExpectedHarnessIdentity: &candidate.Identity}
		if e != nil || !peer.permits("dispatch") || !peer.permits("info") || !peer.permitsTask(intent) {
			candidate.Authorized = false
		}
		if candidate.Authorized {
			preview, e := c.HarnessIdentity(ctx, proposal.Destination, HarnessIdentityRequest{proposal.ModelID, proposal.HarnessID, int(request.ContextTokens)})
			if e != nil {
				candidate.Available = false
			} else if preview.Identity != candidate.Identity {
				candidate.Compatible = false
			}
		}
		scoped = append(scoped, harness.ScopedCandidate{Scope: proposal.Destination, Candidate: candidate})
	}
	// Credential rotation during observation must not borrow another caller's ledger.
	cert, _, err = c.Credentials.load()
	if err != nil || len(cert.Certificate) == 0 || certificateDigest(cert.Certificate[0]) != pin {
		return out, ErrConflict
	}
	now := time.Now().UTC()
	snapshots := map[string]*harness.Snapshot{}
	for i := range scoped {
		destination := scoped[i].Scope
		evidence, ok := snapshots[destination]
		if !ok {
			evidence, err = readDestinationEvidence(ctx, root, destination, pin, now)
			if err != nil {
				return out, err
			}
			snapshots[destination] = evidence
		}
		scoped[i].Evidence = evidence
	}
	selected, err := harness.SelectScoped(request, policy, scoped, now, draw)
	out.Selection = selected
	if err != nil {
		return out, err
	}
	for _, proposal := range candidates {
		if proposal.Destination == selected.Primary.Scope && proposal.Candidate.Identity == selected.Primary.Ranked.Identity {
			out.ModelID = proposal.ModelID
			out.HarnessID = proposal.HarnessID
			break
		}
	}
	return out, nil
}

func readDestinationEvidence(ctx context.Context, root, destination, caller string, now time.Time) (*harness.Snapshot, error) {
	base := &RouteStore{directory: root}
	if err := base.check(); err != nil {
		if _, e := os.Lstat(root); errors.Is(e, os.ErrNotExist) {
			return harness.Replay(nil, nil, now)
		}
		return nil, err
	}
	scope := hash(struct{ Destination, Caller string }{destination, caller})
	dir := &RouteStore{directory: filepath.Join(root, scope)}
	if err := dir.check(); err != nil {
		if _, e := os.Lstat(dir.directory); errors.Is(e, os.ErrNotExist) {
			return harness.Replay(nil, nil, now)
		}
		return nil, err
	}
	ledger, err := harness.OpenEvidenceStoreReadOnly(filepath.Join(dir.directory, "ledger"))
	if err != nil {
		// A receipt may precede the first ledger write. Missing evidence is unknown,
		// never a pass; existing but damaged ledger files are not treated as empty.
		if _, e := os.Lstat(filepath.Join(dir.directory, "ledger")); errors.Is(e, os.ErrNotExist) {
			return harness.Replay(nil, nil, now)
		}
		return nil, err
	}
	defer ledger.Close()
	return ledger.Snapshot(ctx, now)
}
