package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

const (
	PolicySnapshotVersion  = 1
	MaxPolicySnapshotBytes = 64 << 10

	maxPolicySnapshotEntries = 1024
)

var ErrPolicySnapshot = errors.New("invalid tool policy snapshot")

// PolicyPoint names one exact tool and resource scope and, in a sealed
// snapshot, its effective decision. Callers of SealPolicySnapshot supply only
// Tool and Scope; Decision is derived from Policy and must be empty on input.
// Wildcards are policy-language syntax and are never valid point identities.
type PolicyPoint struct {
	Tool     string   `json:"tool"`
	Scope    string   `json:"scope"`
	Decision Decision `json:"decision"`
}

// PolicySnapshot is bounded, canonical evidence over a caller-selected set of
// exact tool/scope targets. It is not a complete serialization of Policy and
// grants no authority by itself.
type PolicySnapshot struct {
	Version int           `json:"version"`
	Entries []PolicyPoint `json:"entries"`
	Digest  string        `json:"digest"`
}

// SealPolicySnapshot resolves every target through an owned, validated policy
// chain, then canonicalizes and owns the resulting evidence. Repeated targets
// have one canonical entry.
func SealPolicySnapshot(policy *Policy, points []PolicyPoint) (PolicySnapshot, error) {
	if len(points) > maxPolicySnapshotEntries {
		return PolicySnapshot{}, ErrPolicySnapshot
	}
	ownedPolicy, err := extensionPolicy(policy)
	if err != nil {
		return PolicySnapshot{}, ErrPolicySnapshot
	}
	unique := make(map[policyPointIdentity]struct{}, len(points))
	for _, point := range points {
		if point.Decision != "" || !validPolicyPoint(point) {
			return PolicySnapshot{}, ErrPolicySnapshot
		}
		unique[policyPointIdentity{Tool: point.Tool, Scope: point.Scope}] = struct{}{}
	}
	entries := make([]PolicyPoint, 0, len(unique))
	for point := range unique {
		entries = append(entries, PolicyPoint{
			Tool: point.Tool, Scope: point.Scope,
			Decision: ownedPolicy.Decide(point.Tool, point.Scope),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return policyEntryLess(entries[i], entries[j])
	})
	snapshot := PolicySnapshot{Version: PolicySnapshotVersion, Entries: entries}
	snapshot.Digest, err = snapshot.canonicalDigest()
	if err != nil || snapshot.Validate() != nil {
		return PolicySnapshot{}, ErrPolicySnapshot
	}
	return snapshot, nil
}

// Validate verifies the canonical order, exact identities, decisions and
// digest of a snapshot. SealPolicySnapshot is responsible for copy ownership;
// validation detects any subsequent caller mutation.
func (s PolicySnapshot) Validate() error {
	if s.Version != PolicySnapshotVersion || s.Entries == nil || len(s.Entries) > maxPolicySnapshotEntries || !validPolicySnapshotDigest(s.Digest) {
		return ErrPolicySnapshot
	}
	for i, entry := range s.Entries {
		if !validPolicyPoint(entry) || !validPolicyDecision(entry.Decision) {
			return ErrPolicySnapshot
		}
		if i > 0 && !policyEntryLess(s.Entries[i-1], entry) {
			return ErrPolicySnapshot
		}
	}
	digest, err := s.canonicalDigest()
	if err != nil || digest != s.Digest {
		return ErrPolicySnapshot
	}
	body, err := json.Marshal(s)
	if err != nil || len(body) > MaxPolicySnapshotBytes {
		return ErrPolicySnapshot
	}
	return nil
}

// AtMost proves that the receiver contains no point absent from parent and
// never has a more permissive effective decision. Omitting a parent point is a
// restriction. Decision strictness is deny < ask < allow.
func (s PolicySnapshot) AtMost(parent PolicySnapshot) bool {
	if parent.Validate() != nil || s.Validate() != nil {
		return false
	}
	parentEntries := make(map[policyPointIdentity]Decision, len(parent.Entries))
	for _, entry := range parent.Entries {
		parentEntries[policyPointIdentity{Tool: entry.Tool, Scope: entry.Scope}] = entry.Decision
	}
	for _, entry := range s.Entries {
		decision, ok := parentEntries[policyPointIdentity{Tool: entry.Tool, Scope: entry.Scope}]
		if !ok || policyDecisionRank(entry.Decision) > policyDecisionRank(decision) {
			return false
		}
	}
	return true
}

// CanonicalJSON returns the validated, complete snapshot envelope. Struct field
// order and canonical entry ordering make these bytes stable across callers.
func (s PolicySnapshot) CanonicalJSON() ([]byte, error) {
	if s.Validate() != nil {
		return nil, ErrPolicySnapshot
	}
	body, err := json.Marshal(s)
	if err != nil {
		return nil, ErrPolicySnapshot
	}
	return body, nil
}

func (s PolicySnapshot) canonicalDigest() (string, error) {
	s.Digest = ""
	body, err := json.Marshal(s)
	if err != nil || len(body) > MaxPolicySnapshotBytes {
		return "", ErrPolicySnapshot
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

type policyPointIdentity struct{ Tool, Scope string }

func validPolicyPoint(point PolicyPoint) bool {
	return point.Tool != "*" && point.Scope != "*" && extensionName(point.Tool) && extensionScope(point.Scope)
}

func validPolicyDecision(decision Decision) bool {
	return decision == Deny || decision == Ask || decision == Allow
}

func validPolicySnapshotDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func policyEntryLess(left, right PolicyPoint) bool {
	return left.Tool < right.Tool || left.Tool == right.Tool && left.Scope < right.Scope
}

func policyDecisionRank(decision Decision) int {
	switch decision {
	case Deny:
		return 0
	case Ask:
		return 1
	case Allow:
		return 2
	default:
		return 3
	}
}
