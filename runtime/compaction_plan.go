package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

const (
	ContextCompactionPlanVersion  = 1
	MaxContextCompactionPlanBytes = 8 << 20
	maxCompactionPlanMessages     = 100_000
)

var ErrInvalidCompactionPlan = errors.New("invalid context compaction plan")

// ContextEngineIdentity pins the implementation that selected a compaction.
// Digest is the SHA-256 of the canonical version, ID and revision payload.
type ContextEngineIdentity struct {
	Version  int    `json:"version"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Digest   string `json:"digest"`
}

func NewContextEngineIdentity(id, revision string) (ContextEngineIdentity, error) {
	identity := ContextEngineIdentity{Version: ContextCompactionPlanVersion, ID: id, Revision: revision}
	digest, err := identity.CanonicalDigest()
	if err != nil {
		return ContextEngineIdentity{}, err
	}
	identity.Digest = digest
	return identity, nil
}

func (i ContextEngineIdentity) CanonicalDigest() (string, error) {
	if i.Version != ContextCompactionPlanVersion || !validCompactionPlanLabel(i.ID) || !validCompactionPlanLabel(i.Revision) {
		return "", ErrInvalidCompactionPlan
	}
	return canonicalCompactionPlanDigest(struct {
		Version  int    `json:"version"`
		ID       string `json:"id"`
		Revision string `json:"revision"`
	}{i.Version, i.ID, i.Revision})
}

func (i ContextEngineIdentity) Validate() error {
	digest, err := i.CanonicalDigest()
	if err != nil || !validCompactionPlanDigest(i.Digest) || digest != i.Digest {
		return ErrInvalidCompactionPlan
	}
	return nil
}

type ContextTier string

const (
	ContextTierStable   ContextTier = "stable"
	ContextTierProject  ContextTier = "project"
	ContextTierVolatile ContextTier = "volatile"
)

// ContextTierPlan pins the three host-owned prompt tiers and their inclusion
// order. Stable authority and volatile task input are mandatory; project
// context is optional but, when present, remains between them.
type ContextTierPlan struct {
	Version        int           `json:"version"`
	StableDigest   string        `json:"stable_digest"`
	ProjectDigest  string        `json:"project_digest"`
	VolatileDigest string        `json:"volatile_digest"`
	Included       []ContextTier `json:"included"`
	Digest         string        `json:"digest"`
}

func NewContextTierPlan(stable, project, volatile string, included []ContextTier) (ContextTierPlan, error) {
	plan := ContextTierPlan{
		Version: ContextCompactionPlanVersion, StableDigest: stable,
		ProjectDigest: project, VolatileDigest: volatile,
		Included: append([]ContextTier(nil), included...),
	}
	digest, err := plan.CanonicalDigest()
	if err != nil {
		return ContextTierPlan{}, err
	}
	plan.Digest = digest
	return plan, nil
}

func (p ContextTierPlan) CanonicalDigest() (string, error) {
	if p.Version != ContextCompactionPlanVersion || !validCompactionPlanDigest(p.StableDigest) ||
		!validCompactionPlanDigest(p.ProjectDigest) || !validCompactionPlanDigest(p.VolatileDigest) || !validContextTierOrder(p.Included) {
		return "", ErrInvalidCompactionPlan
	}
	return canonicalCompactionPlanDigest(struct {
		Version        int           `json:"version"`
		StableDigest   string        `json:"stable_digest"`
		ProjectDigest  string        `json:"project_digest"`
		VolatileDigest string        `json:"volatile_digest"`
		Included       []ContextTier `json:"included"`
	}{p.Version, p.StableDigest, p.ProjectDigest, p.VolatileDigest, p.Included})
}

func (p ContextTierPlan) Validate() error {
	digest, err := p.CanonicalDigest()
	if err != nil || !validCompactionPlanDigest(p.Digest) || digest != p.Digest {
		return ErrInvalidCompactionPlan
	}
	return nil
}

// ContextCompactionPlan is immutable structural admission evidence for one
// future prefix replacement. Validate proves only bounded canonical structure
// and internal digests; it does not prove that supplied operation, request,
// configuration, policy, draft, tier, source, or review identities are genuine.
// Durable storage must derive and cross-bind those facts before activation.
// The plan grants no approval itself: Compaction must name a currently approved
// summary attempt and review, which storage revalidates at activation.
type ContextCompactionPlan struct {
	Version                 int                   `json:"version"`
	OperationID             string                `json:"operation_id"`
	OperationDigest         string                `json:"operation_digest"`
	RequestID               string                `json:"request_id"`
	RequestDigest           string                `json:"request_digest"`
	Compaction              *ContextCompaction    `json:"compaction"`
	ConfigDigest            string                `json:"config_digest"`
	PolicyDigest            string                `json:"policy_digest"`
	Engine                  ContextEngineIdentity `json:"engine"`
	Tiers                   ContextTierPlan       `json:"tiers"`
	OriginalPrefix          []providers.Message   `json:"original_prefix"`
	OriginalPrefixDigest    string                `json:"original_prefix_digest"`
	ReplacementPrefix       []providers.Message   `json:"replacement_prefix"`
	ReplacementPrefixDigest string                `json:"replacement_prefix_digest"`
	LiveSuffixBoundary      int                   `json:"live_suffix_boundary"`
	// LiveSuffixBoundaryDigest binds the prepared original-prefix boundary.
	// Future live suffix content cannot be known yet; activation must bind its
	// actual count and digest in separate durable evidence.
	LiveSuffixBoundaryDigest string `json:"live_suffix_boundary_digest"`
	DraftDigest              string `json:"draft_digest"`
	PlanDigest               string `json:"plan_digest"`
}

// SealContextCompactionPlan owns all nested values, fixes the version, and
// computes the derived boundary and plan digests. It does not repair invalid
// evidence, boundaries, or tier identities.
func SealContextCompactionPlan(input ContextCompactionPlan) (ContextCompactionPlan, error) {
	input.Version = ContextCompactionPlanVersion
	input.PlanDigest = ""
	input.LiveSuffixBoundaryDigest = ""
	input.OriginalPrefixDigest = ""
	input.ReplacementPrefixDigest = ""
	var err error
	input.OriginalPrefixDigest, err = contextCompactionPrefixDigest(input.OriginalPrefix)
	if err != nil {
		return ContextCompactionPlan{}, err
	}
	input.ReplacementPrefixDigest, err = contextCompactionPrefixDigest(input.ReplacementPrefix)
	if err != nil || input.OriginalPrefixDigest == input.ReplacementPrefixDigest {
		return ContextCompactionPlan{}, ErrInvalidCompactionPlan
	}
	input.LiveSuffixBoundaryDigest, err = ContextCompactionBoundaryDigest(input.OriginalPrefix, input.LiveSuffixBoundary)
	if err != nil {
		return ContextCompactionPlan{}, err
	}
	if input.validateEvidence() != nil {
		return ContextCompactionPlan{}, ErrInvalidCompactionPlan
	}
	owned, err := cloneContextCompactionPlan(input)
	if err != nil {
		return ContextCompactionPlan{}, err
	}
	digest, err := owned.CanonicalDigest()
	if err != nil {
		return ContextCompactionPlan{}, err
	}
	owned.PlanDigest = digest
	if owned.Validate() != nil {
		return ContextCompactionPlan{}, ErrInvalidCompactionPlan
	}
	return owned, nil
}

func (p ContextCompactionPlan) CanonicalDigest() (string, error) {
	if p.validateEvidence() != nil {
		return "", ErrInvalidCompactionPlan
	}
	p.PlanDigest = ""
	body, err := json.Marshal(p)
	if err != nil || len(body) > MaxContextCompactionPlanBytes {
		return "", ErrInvalidCompactionPlan
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (p ContextCompactionPlan) Validate() error {
	body, encodeErr := json.Marshal(p)
	digest, err := p.CanonicalDigest()
	if encodeErr != nil || len(body) > MaxContextCompactionPlanBytes || err != nil ||
		!validCompactionPlanDigest(p.PlanDigest) || digest != p.PlanDigest {
		return ErrInvalidCompactionPlan
	}
	return nil
}

func (p ContextCompactionPlan) validateEvidence() error {
	if p.Version != ContextCompactionPlanVersion || !validCompactionPlanLabel(p.OperationID) || !validCompactionPlanLabel(p.RequestID) ||
		!validCompactionPlanDigest(p.OperationDigest) || !validCompactionPlanDigest(p.RequestDigest) || !validCompactionPlanDigest(p.ConfigDigest) ||
		!validCompactionPlanDigest(p.PolicyDigest) || !validCompactionPlanDigest(p.DraftDigest) ||
		p.Engine.Validate() != nil || p.Tiers.Validate() != nil || p.Compaction == nil ||
		p.Compaction.SummaryAttemptID == "" || p.Compaction.SummaryReviewID == "" ||
		p.Compaction.Validate(p.Compaction.SourceTaskID) != nil ||
		p.Compaction.FirstRetainedMessage < 1 || p.Compaction.FirstRetainedMessage < p.Compaction.RemovedMessages ||
		p.Compaction.FirstRetainedSequence < 1 || p.Compaction.FirstRetainedSequence > p.Compaction.SourceSequence ||
		p.Compaction.BeforeContextTokens <= p.Compaction.AfterContextTokens ||
		p.LiveSuffixBoundary != len(p.OriginalPrefix) ||
		!validCompactionPlanMessages(p.OriginalPrefix) || !validCompactionPlanMessages(p.ReplacementPrefix) {
		return ErrInvalidCompactionPlan
	}
	originalDigest, err := contextCompactionPrefixDigest(p.OriginalPrefix)
	if err != nil || !validCompactionPlanDigest(p.OriginalPrefixDigest) || originalDigest != p.OriginalPrefixDigest {
		return ErrInvalidCompactionPlan
	}
	replacementDigest, err := contextCompactionPrefixDigest(p.ReplacementPrefix)
	if err != nil || !validCompactionPlanDigest(p.ReplacementPrefixDigest) || replacementDigest != p.ReplacementPrefixDigest || replacementDigest == originalDigest {
		return ErrInvalidCompactionPlan
	}
	boundaryDigest, err := contextCompactionBoundaryDigest(p.OriginalPrefix, p.LiveSuffixBoundary)
	if err != nil || !validCompactionPlanDigest(p.LiveSuffixBoundaryDigest) || boundaryDigest != p.LiveSuffixBoundaryDigest {
		return ErrInvalidCompactionPlan
	}
	return nil
}

// ContextCompactionBoundaryDigest canonically binds the exact prefix and the
// message boundary after it. It intentionally says nothing about later suffix.
func ContextCompactionBoundaryDigest(prefix []providers.Message, boundary int) (string, error) {
	if !validCompactionPlanMessages(prefix) || boundary != len(prefix) {
		return "", ErrInvalidCompactionPlan
	}
	return contextCompactionBoundaryDigest(prefix, boundary)
}

func contextCompactionBoundaryDigest(prefix []providers.Message, boundary int) (string, error) {
	prefixDigest, err := contextCompactionPrefixDigest(prefix)
	if err != nil {
		return "", ErrInvalidCompactionPlan
	}
	return canonicalCompactionPlanDigest(struct {
		OriginalPrefixDigest string `json:"original_prefix_digest"`
		Boundary             int    `json:"boundary"`
	}{prefixDigest, boundary})
}

func contextCompactionPrefixDigest(prefix []providers.Message) (string, error) {
	body, err := json.Marshal(prefix)
	if err != nil || len(body) > MaxContextCompactionPlanBytes {
		return "", ErrInvalidCompactionPlan
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func validContextTierOrder(tiers []ContextTier) bool {
	if len(tiers) == 2 {
		return tiers[0] == ContextTierStable && tiers[1] == ContextTierVolatile
	}
	return len(tiers) == 3 && tiers[0] == ContextTierStable && tiers[1] == ContextTierProject && tiers[2] == ContextTierVolatile
}

func validCompactionPlanMessages(messages []providers.Message) bool {
	if len(messages) < 1 || len(messages) > maxCompactionPlanMessages {
		return false
	}
	rawBytes := 0
	for _, message := range messages {
		if !utf8.ValidString(message.Role) || !utf8.ValidString(message.Content) || !utf8.ValidString(message.ToolCallID) {
			return false
		}
		rawBytes += len(message.Role) + len(message.Content) + len(message.ToolCallID)
		for _, call := range message.ToolCalls {
			if !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) || !utf8.Valid(call.Arguments) {
				return false
			}
			rawBytes += len(call.ID) + len(call.Name) + len(call.Arguments)
		}
		if rawBytes > MaxContextCompactionPlanBytes {
			return false
		}
	}
	return providers.ValidateMessages(messages) == nil
}

func validCompactionPlanLabel(value string) bool {
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

func validCompactionPlanDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonicalCompactionPlanDigest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body) > MaxContextCompactionPlanBytes {
		return "", ErrInvalidCompactionPlan
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func cloneContextCompactionPlan(input ContextCompactionPlan) (ContextCompactionPlan, error) {
	body, err := json.Marshal(input)
	if err != nil || len(body) > MaxContextCompactionPlanBytes {
		return ContextCompactionPlan{}, ErrInvalidCompactionPlan
	}
	var owned ContextCompactionPlan
	if json.Unmarshal(body, &owned) != nil {
		return ContextCompactionPlan{}, ErrInvalidCompactionPlan
	}
	return owned, nil
}
