package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	DelegationCompactionAuthorityVersion  = 1
	MaxDelegationCompactionAuthorityBytes = 128 << 10
	maxDelegationCompactionPolicyBytes    = 64 << 10
)

var ErrDelegationCompactionAuthority = errors.New("invalid delegation compaction authority")

// DelegationCompactionAuthority is immutable, redaction-safe authority emitted
// when a bounded worker starts. It binds the parent plan and inherited policy
// without guessing child execution identities that do not exist yet. Storage
// must independently derive those identities from the worker and child journals
// before accepting a later compaction activation.
type DelegationCompactionAuthority struct {
	Version               int             `json:"version"`
	RootTaskID            string          `json:"root_task_id"`
	PlanDigest            string          `json:"plan_digest"`
	InheritedEngineDigest string          `json:"inherited_engine_digest"`
	Scope                 string          `json:"scope"`
	ParentPolicy          json.RawMessage `json:"parent_policy"`
	ParentPolicyDigest    string          `json:"parent_policy_digest"`
	ChildPolicy           json.RawMessage `json:"child_policy"`
	ChildPolicyDigest     string          `json:"child_policy_digest"`
	AuthorityDigest       string          `json:"authority_digest"`
}

// SealDelegationCompactionAuthority canonicalizes and owns both policy
// snapshots, derives their digests, and seals the complete envelope. Supplied
// derived fields are deliberately ignored.
func SealDelegationCompactionAuthority(input DelegationCompactionAuthority) (DelegationCompactionAuthority, error) {
	input.Version = DelegationCompactionAuthorityVersion
	input.ParentPolicyDigest, input.ChildPolicyDigest, input.AuthorityDigest = "", "", ""
	var err error
	input.ParentPolicy, input.ParentPolicyDigest, err = canonicalDelegationCompactionPolicy(input.ParentPolicy)
	if err != nil {
		return DelegationCompactionAuthority{}, err
	}
	input.ChildPolicy, input.ChildPolicyDigest, err = canonicalDelegationCompactionPolicy(input.ChildPolicy)
	if err != nil {
		return DelegationCompactionAuthority{}, err
	}
	digest, err := input.CanonicalDigest()
	if err != nil {
		return DelegationCompactionAuthority{}, err
	}
	input.AuthorityDigest = digest
	if input.Validate() != nil {
		return DelegationCompactionAuthority{}, ErrDelegationCompactionAuthority
	}
	return input.cloneValue(), nil
}

// CanonicalDigest derives the envelope identity without its derived digest.
func (a DelegationCompactionAuthority) CanonicalDigest() (string, error) {
	if a.validateBase() != nil {
		return "", ErrDelegationCompactionAuthority
	}
	a.AuthorityDigest = ""
	body, err := json.Marshal(a)
	if err != nil || len(body) > MaxDelegationCompactionAuthorityBytes {
		return "", ErrDelegationCompactionAuthority
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (a DelegationCompactionAuthority) Validate() error {
	digest, err := a.CanonicalDigest()
	if err != nil || !validDelegationCompactionDigest(a.AuthorityDigest) || digest != a.AuthorityDigest {
		return ErrDelegationCompactionAuthority
	}
	body, err := json.Marshal(a)
	if err != nil || len(body) > MaxDelegationCompactionAuthorityBytes {
		return ErrDelegationCompactionAuthority
	}
	return nil
}

// Clone returns a fully owned copy suitable for crossing extension boundaries.
func (a DelegationCompactionAuthority) Clone() *DelegationCompactionAuthority {
	owned := a.cloneValue()
	return &owned
}

func (a DelegationCompactionAuthority) cloneValue() DelegationCompactionAuthority {
	a.ParentPolicy = append(json.RawMessage(nil), a.ParentPolicy...)
	a.ChildPolicy = append(json.RawMessage(nil), a.ChildPolicy...)
	return a
}

func (a DelegationCompactionAuthority) validateBase() error {
	parent, parentDigest, err := canonicalDelegationCompactionPolicy(a.ParentPolicy)
	if err != nil || !bytes.Equal(parent, a.ParentPolicy) || parentDigest != a.ParentPolicyDigest {
		return ErrDelegationCompactionAuthority
	}
	child, childDigest, err := canonicalDelegationCompactionPolicy(a.ChildPolicy)
	if err != nil || !bytes.Equal(child, a.ChildPolicy) || childDigest != a.ChildPolicyDigest {
		return ErrDelegationCompactionAuthority
	}
	if a.Version != DelegationCompactionAuthorityVersion || !validDelegationCompactionID(a.RootTaskID) ||
		!validDelegationCompactionDigest(a.PlanDigest) || !validDelegationCompactionDigest(a.InheritedEngineDigest) ||
		a.Scope != "delegation-"+a.RootTaskID || len(a.Scope) > 512 {
		return ErrDelegationCompactionAuthority
	}
	return nil
}

func canonicalDelegationCompactionPolicy(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(raw) == 0 || len(raw) > maxDelegationCompactionPolicyBytes {
		return nil, "", ErrDelegationCompactionAuthority
	}
	canonical, err := canonicalToolArguments(raw)
	if err != nil || len(canonical) == 0 || len(canonical) > maxDelegationCompactionPolicyBytes {
		return nil, "", ErrDelegationCompactionAuthority
	}
	sum := sha256.Sum256(canonical)
	return append(json.RawMessage(nil), canonical...), hex.EncodeToString(sum[:]), nil
}

func validDelegationCompactionID(value string) bool {
	if len(value) < 1 || len(value) > 128 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validDelegationCompactionDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
