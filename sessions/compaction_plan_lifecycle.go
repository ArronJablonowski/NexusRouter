package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

const (
	ContextCompactionLifecycleVersion  = 1
	MaxContextCompactionLifecycleBytes = 16 << 20
	maxContextCompactionSnapshotBytes  = 1 << 20
)

var ErrContextCompactionLifecycle = errors.New("invalid context compaction lifecycle")

type ContextCompactionLifecycleKind string

const (
	ContextCompactionStarted   ContextCompactionLifecycleKind = "started"
	ContextCompactionPrepared  ContextCompactionLifecycleKind = "prepared"
	ContextCompactionValidated ContextCompactionLifecycleKind = "validated"
	ContextCompactionApproved  ContextCompactionLifecycleKind = "approved"
	ContextCompactionRevoked   ContextCompactionLifecycleKind = "revoked"
	ContextCompactionActivated ContextCompactionLifecycleKind = "activated"
	ContextCompactionFailed    ContextCompactionLifecycleKind = "failed"
)

// ContextCompactionPlanStart is the immutable, host-owned request recorded
// before summary inference. ConfigSnapshot and PolicySnapshot must be reduced,
// non-secret JSON authority snapshots rather than application configuration.
type ContextCompactionPlanStart struct {
	Version         int                            `json:"version"`
	OperationID     string                         `json:"operation_id"`
	OperationDigest string                         `json:"operation_digest"`
	RequestID       string                         `json:"request_id"`
	RequestDigest   string                         `json:"request_digest"`
	TaskID          string                         `json:"task_id"`
	SourceSequence  int64                          `json:"source_sequence"`
	SourceDigest    string                         `json:"source_digest"`
	AttemptID       string                         `json:"attempt_id"`
	Model           string                         `json:"model"`
	Provider        string                         `json:"provider"`
	Keep            int                            `json:"keep"`
	EstimatedCost   float64                        `json:"estimated_cost"`
	ConfigSnapshot  json.RawMessage                `json:"config_snapshot"`
	ConfigDigest    string                         `json:"config_digest"`
	PolicySnapshot  json.RawMessage                `json:"policy_snapshot"`
	PolicyDigest    string                         `json:"policy_digest"`
	Engine          runtime.ContextEngineIdentity  `json:"engine"`
	Tiers           runtime.ContextTierPlan        `json:"tiers"`
	ProcessID       string                         `json:"process_id"`
	StartedAt       time.Time                      `json:"started_at"`
	Status          ContextCompactionLifecycleKind `json:"status"`
}

// SealContextCompactionPlanStart canonicalizes owned JSON snapshots and derives
// both request and operation digests. Caller-provided digest fields are ignored.
func SealContextCompactionPlanStart(in ContextCompactionPlanStart) (ContextCompactionPlanStart, error) {
	in.Version = ContextCompactionLifecycleVersion
	in.Status = ContextCompactionStarted
	in.OperationDigest, in.RequestDigest, in.ConfigDigest, in.PolicyDigest = "", "", "", ""
	in.StartedAt = in.StartedAt.UTC()
	var err error
	in.ConfigSnapshot, in.ConfigDigest, err = canonicalLifecycleSnapshot(in.ConfigSnapshot)
	if err != nil {
		return ContextCompactionPlanStart{}, err
	}
	in.PolicySnapshot, in.PolicyDigest, err = canonicalLifecycleSnapshot(in.PolicySnapshot)
	if err != nil || in.validateBase() != nil {
		return ContextCompactionPlanStart{}, ErrContextCompactionLifecycle
	}
	in.RequestDigest, err = in.CanonicalRequestDigest()
	if err != nil {
		return ContextCompactionPlanStart{}, err
	}
	in.OperationDigest, err = in.CanonicalDigest()
	if err != nil || in.Validate() != nil {
		return ContextCompactionPlanStart{}, ErrContextCompactionLifecycle
	}
	return cloneLifecycle(in)
}

// CanonicalRequestDigest binds the caller request independently of its owning
// process and start time, allowing a repository to detect reused request IDs.
func (s ContextCompactionPlanStart) CanonicalRequestDigest() (string, error) {
	if s.validateBase() != nil {
		return "", ErrContextCompactionLifecycle
	}
	return lifecycleDigest(struct {
		Version                                              int `json:"version"`
		TaskID, SourceDigest                                 string
		SourceSequence                                       int64
		Model, Provider                                      string
		Keep                                                 int
		EstimatedCost                                        float64
		ConfigDigest, PolicyDigest, EngineDigest, TierDigest string
	}{s.Version, s.TaskID, s.SourceDigest, s.SourceSequence, s.Model, s.Provider,
		s.Keep, s.EstimatedCost, s.ConfigDigest, s.PolicyDigest, s.Engine.Digest, s.Tiers.Digest})
}

func (s ContextCompactionPlanStart) CanonicalDigest() (string, error) {
	if s.validateBase() != nil || !validLifecycleDigest(s.RequestDigest) {
		return "", ErrContextCompactionLifecycle
	}
	s.OperationDigest = ""
	return lifecycleDigest(s)
}

func (s ContextCompactionPlanStart) Validate() error {
	request, err := s.CanonicalRequestDigest()
	if err != nil || request != s.RequestDigest || !validLifecycleDigest(s.RequestDigest) {
		return ErrContextCompactionLifecycle
	}
	operation, err := s.CanonicalDigest()
	if err != nil || operation != s.OperationDigest || !validLifecycleDigest(s.OperationDigest) {
		return ErrContextCompactionLifecycle
	}
	return lifecycleSize(s)
}

func (s ContextCompactionPlanStart) validateBase() error {
	config, configDigest, err := canonicalLifecycleSnapshot(s.ConfigSnapshot)
	if err != nil || !bytes.Equal(config, s.ConfigSnapshot) || configDigest != s.ConfigDigest {
		return ErrContextCompactionLifecycle
	}
	policy, policyDigest, err := canonicalLifecycleSnapshot(s.PolicySnapshot)
	if err != nil || !bytes.Equal(policy, s.PolicySnapshot) || policyDigest != s.PolicyDigest {
		return ErrContextCompactionLifecycle
	}
	if s.Version != ContextCompactionLifecycleVersion || s.Status != ContextCompactionStarted ||
		!validLifecycleID(s.OperationID) || !validLifecycleID(s.RequestID) || !validLifecycleID(s.TaskID) ||
		!validLifecycleID(s.AttemptID) || !validLifecycleID(s.Model) || !validLifecycleID(s.Provider) ||
		!validLifecycleID(s.ProcessID) || s.SourceSequence < 1 || !validLifecycleDigest(s.SourceDigest) ||
		s.Keep < 1 || s.Keep > 100000 || math.IsNaN(s.EstimatedCost) || math.IsInf(s.EstimatedCost, 0) ||
		s.EstimatedCost < 0 || s.Engine.Validate() != nil || s.Tiers.Validate() != nil ||
		s.StartedAt.IsZero() || s.StartedAt.Location() != time.UTC {
		return ErrContextCompactionLifecycle
	}
	return nil
}

// ContextCompactionActivation binds suffix evidence that was necessarily
// unknown when the immutable plan was prepared.
type ContextCompactionActivation struct {
	Version            int       `json:"version"`
	OperationID        string    `json:"operation_id"`
	PlanDigest         string    `json:"plan_digest"`
	TaskID             string    `json:"task_id"`
	EventID            string    `json:"event_id"`
	EventSequence      int64     `json:"event_sequence"`
	LiveSuffixBoundary int       `json:"live_suffix_boundary"`
	LiveSuffixCount    int       `json:"live_suffix_count"`
	LiveSuffixDigest   string    `json:"live_suffix_digest"`
	ActivatedAt        time.Time `json:"activated_at"`
	ActivationDigest   string    `json:"activation_digest"`
}

func SealContextCompactionActivation(in ContextCompactionActivation) (ContextCompactionActivation, error) {
	in.Version, in.ActivationDigest = ContextCompactionLifecycleVersion, ""
	in.ActivatedAt = in.ActivatedAt.UTC()
	digest, err := in.CanonicalDigest()
	if err != nil {
		return ContextCompactionActivation{}, err
	}
	in.ActivationDigest = digest
	if in.Validate() != nil {
		return ContextCompactionActivation{}, ErrContextCompactionLifecycle
	}
	return in, nil
}

func (a ContextCompactionActivation) CanonicalDigest() (string, error) {
	if a.Version != ContextCompactionLifecycleVersion || !validLifecycleID(a.OperationID) || !validLifecycleID(a.TaskID) ||
		!validLifecycleID(a.EventID) || !validLifecycleDigest(a.PlanDigest) || !validLifecycleDigest(a.LiveSuffixDigest) ||
		a.EventSequence < 1 || a.LiveSuffixBoundary < 1 || a.LiveSuffixCount < 0 || a.LiveSuffixCount > 100000 ||
		a.ActivatedAt.IsZero() || a.ActivatedAt.Location() != time.UTC {
		return "", ErrContextCompactionLifecycle
	}
	a.ActivationDigest = ""
	return lifecycleDigest(a)
}

func (a ContextCompactionActivation) Validate() error {
	digest, err := a.CanonicalDigest()
	if err != nil || !validLifecycleDigest(a.ActivationDigest) || digest != a.ActivationDigest {
		return ErrContextCompactionLifecycle
	}
	return lifecycleSize(a)
}

// ContextCompactionLiveSuffixDigest canonically binds the exact live suffix;
// nil and an empty suffix deliberately have one representation.
func ContextCompactionLiveSuffixDigest(messages []providers.Message) (string, error) {
	if messages == nil {
		messages = []providers.Message{}
	}
	if len(messages) > 100000 || (len(messages) > 0 && providers.ValidateMessages(messages) != nil) {
		return "", ErrContextCompactionLifecycle
	}
	return lifecycleDigest(messages)
}

// ContextCompactionLifecycleFact is an immutable state transition. Validation
// is structural; repositories must compare it with durable evidence in one
// serialized transaction.
type ContextCompactionLifecycleFact struct {
	Version          int                            `json:"version"`
	ID               string                         `json:"id"`
	Digest           string                         `json:"digest"`
	OperationID      string                         `json:"operation_id"`
	Sequence         int                            `json:"sequence"`
	PreviousID       string                         `json:"previous_id,omitempty"`
	Kind             ContextCompactionLifecycleKind `json:"kind"`
	PlanDigest       string                         `json:"plan_digest,omitempty"`
	SummaryAttemptID string                         `json:"summary_attempt_id,omitempty"`
	SummaryReviewID  string                         `json:"summary_review_id,omitempty"`
	Activation       *ContextCompactionActivation   `json:"activation,omitempty"`
	Code             string                         `json:"code,omitempty"`
	CreatedAt        time.Time                      `json:"created_at"`
}

func SealContextCompactionLifecycleFact(in ContextCompactionLifecycleFact) (ContextCompactionLifecycleFact, error) {
	in.Version, in.Digest = ContextCompactionLifecycleVersion, ""
	in.CreatedAt = in.CreatedAt.UTC()
	if in.Activation != nil {
		copy := *in.Activation
		in.Activation = &copy
	}
	digest, err := in.CanonicalDigest()
	if err != nil {
		return ContextCompactionLifecycleFact{}, err
	}
	in.Digest = digest
	if in.Validate() != nil {
		return ContextCompactionLifecycleFact{}, ErrContextCompactionLifecycle
	}
	return in, nil
}

func (f ContextCompactionLifecycleFact) CanonicalDigest() (string, error) {
	if f.validateBase() != nil {
		return "", ErrContextCompactionLifecycle
	}
	f.Digest = ""
	return lifecycleDigest(f)
}

func (f ContextCompactionLifecycleFact) Validate() error {
	digest, err := f.CanonicalDigest()
	if err != nil || !validLifecycleDigest(f.Digest) || digest != f.Digest {
		return ErrContextCompactionLifecycle
	}
	return lifecycleSize(f)
}

func (f ContextCompactionLifecycleFact) validateBase() error {
	if f.Version != ContextCompactionLifecycleVersion || !validLifecycleID(f.ID) || !validLifecycleID(f.OperationID) ||
		f.Sequence < 1 || f.Sequence > 7 || f.CreatedAt.IsZero() || f.CreatedAt.Location() != time.UTC ||
		(f.Sequence == 1) != (f.PreviousID == "") || (f.PreviousID != "" && !validLifecycleID(f.PreviousID)) {
		return ErrContextCompactionLifecycle
	}
	prepared := f.Kind == ContextCompactionPrepared || f.Kind == ContextCompactionValidated || f.Kind == ContextCompactionApproved || f.Kind == ContextCompactionRevoked || f.Kind == ContextCompactionActivated
	hasPlan := validLifecycleDigest(f.PlanDigest) && validLifecycleID(f.SummaryAttemptID) && validLifecycleID(f.SummaryReviewID)
	if prepared && !hasPlan || !prepared && f.Kind != ContextCompactionFailed && (f.PlanDigest != "" || f.SummaryAttemptID != "" || f.SummaryReviewID != "") {
		return ErrContextCompactionLifecycle
	}
	switch f.Kind {
	case ContextCompactionStarted, ContextCompactionPrepared, ContextCompactionValidated, ContextCompactionApproved:
		if f.Code != "" || f.Activation != nil {
			return ErrContextCompactionLifecycle
		}
	case ContextCompactionRevoked:
		if !validLifecycleCode(f.Code) || f.Activation != nil {
			return ErrContextCompactionLifecycle
		}
	case ContextCompactionActivated:
		if f.Code != "" || f.Activation == nil || f.Activation.Validate() != nil ||
			f.Activation.OperationID != f.OperationID || f.Activation.PlanDigest != f.PlanDigest {
			return ErrContextCompactionLifecycle
		}
	case ContextCompactionFailed:
		if !validLifecycleCode(f.Code) || f.Activation != nil ||
			(f.PlanDigest != "" && (!validLifecycleDigest(f.PlanDigest) || !validLifecycleID(f.SummaryAttemptID) || !validLifecycleID(f.SummaryReviewID))) ||
			(f.PlanDigest == "" && (f.SummaryAttemptID != "" || f.SummaryReviewID != "")) {
			return ErrContextCompactionLifecycle
		}
	default:
		return ErrContextCompactionLifecycle
	}
	return nil
}

func ValidateContextCompactionTransition(previous *ContextCompactionLifecycleFact, next ContextCompactionLifecycleFact) error {
	if next.Validate() != nil {
		return ErrContextCompactionLifecycle
	}
	if previous == nil {
		if next.Kind != ContextCompactionStarted || next.Sequence != 1 || next.PreviousID != "" || next.PlanDigest != "" {
			return ErrContextCompactionLifecycle
		}
		return nil
	}
	if previous.Validate() != nil || previous.OperationID != next.OperationID || next.Sequence != previous.Sequence+1 ||
		next.PreviousID != previous.ID || next.CreatedAt.Before(previous.CreatedAt) {
		return ErrContextCompactionLifecycle
	}
	allowed := false
	switch previous.Kind {
	case ContextCompactionStarted:
		allowed = next.Kind == ContextCompactionPrepared || next.Kind == ContextCompactionFailed
	case ContextCompactionPrepared:
		allowed = next.Kind == ContextCompactionValidated || next.Kind == ContextCompactionFailed
	case ContextCompactionValidated:
		allowed = next.Kind == ContextCompactionApproved || next.Kind == ContextCompactionRevoked || next.Kind == ContextCompactionFailed
	case ContextCompactionApproved:
		allowed = next.Kind == ContextCompactionActivated || next.Kind == ContextCompactionRevoked || next.Kind == ContextCompactionFailed
	}
	if !allowed {
		return ErrContextCompactionLifecycle
	}
	if previous.PlanDigest != "" && (next.PlanDigest != previous.PlanDigest || next.SummaryAttemptID != previous.SummaryAttemptID || next.SummaryReviewID != previous.SummaryReviewID) {
		return ErrContextCompactionLifecycle
	}
	return nil
}

type ContextCompactionRecovery struct {
	Version          int       `json:"version"`
	ID               string    `json:"id"`
	Digest           string    `json:"digest"`
	OperationID      string    `json:"operation_id"`
	ProcessID        string    `json:"process_id"`
	FailedFactID     string    `json:"failed_fact_id"`
	FailedFactDigest string    `json:"failed_fact_digest"`
	Reason           string    `json:"reason"`
	RecoveredAt      time.Time `json:"recovered_at"`
}

func SealContextCompactionRecovery(in ContextCompactionRecovery) (ContextCompactionRecovery, error) {
	in.Version, in.Digest = ContextCompactionLifecycleVersion, ""
	in.Reason = "owner_interrupted"
	in.RecoveredAt = in.RecoveredAt.UTC()
	digest, err := in.CanonicalDigest()
	if err != nil {
		return ContextCompactionRecovery{}, err
	}
	in.Digest = digest
	if in.Validate() != nil {
		return ContextCompactionRecovery{}, ErrContextCompactionLifecycle
	}
	return in, nil
}

func (r ContextCompactionRecovery) CanonicalDigest() (string, error) {
	if r.Version != ContextCompactionLifecycleVersion || !validLifecycleID(r.ID) || !validLifecycleID(r.OperationID) ||
		!validLifecycleID(r.ProcessID) || !validLifecycleID(r.FailedFactID) || !validLifecycleDigest(r.FailedFactDigest) ||
		r.Reason != "owner_interrupted" || r.RecoveredAt.IsZero() || r.RecoveredAt.Location() != time.UTC {
		return "", ErrContextCompactionLifecycle
	}
	r.Digest = ""
	return lifecycleDigest(r)
}

func (r ContextCompactionRecovery) Validate() error {
	digest, err := r.CanonicalDigest()
	if err != nil || !validLifecycleDigest(r.Digest) || digest != r.Digest {
		return ErrContextCompactionLifecycle
	}
	return lifecycleSize(r)
}

// ContextCompactionOperationState is a canonical read model. Its validation is
// structural and cross-binding only; durable repositories remain responsible
// for source replay, review-head and process-liveness evidence.
type ContextCompactionOperationState struct {
	Version         int                              `json:"version"`
	Start           ContextCompactionPlanStart       `json:"start"`
	Status          ContextCompactionLifecycleKind   `json:"status"`
	TerminalAttempt *SummaryAttempt                  `json:"terminal_attempt,omitempty"`
	Plan            *runtime.ContextCompactionPlan   `json:"plan,omitempty"`
	Facts           []ContextCompactionLifecycleFact `json:"facts"`
	Recovery        *ContextCompactionRecovery       `json:"recovery,omitempty"`
	StateDigest     string                           `json:"state_digest"`
}

func SealContextCompactionOperationState(in ContextCompactionOperationState) (ContextCompactionOperationState, error) {
	in.Version, in.StateDigest = ContextCompactionLifecycleVersion, ""
	digest, err := in.CanonicalDigest()
	if err != nil {
		return ContextCompactionOperationState{}, err
	}
	in.StateDigest = digest
	if in.Validate() != nil {
		return ContextCompactionOperationState{}, ErrContextCompactionLifecycle
	}
	return cloneLifecycle(in)
}

func (s ContextCompactionOperationState) CanonicalDigest() (string, error) {
	if s.validateBase() != nil {
		return "", ErrContextCompactionLifecycle
	}
	s.StateDigest = ""
	return lifecycleDigest(s)
}

func (s ContextCompactionOperationState) Validate() error {
	digest, err := s.CanonicalDigest()
	if err != nil || !validLifecycleDigest(s.StateDigest) || digest != s.StateDigest {
		return ErrContextCompactionLifecycle
	}
	return lifecycleSize(s)
}

func (s ContextCompactionOperationState) validateBase() error {
	if s.Version != ContextCompactionLifecycleVersion || s.Start.Validate() != nil || len(s.Facts) < 1 || len(s.Facts) > 7 {
		return ErrContextCompactionLifecycle
	}
	seen := map[ContextCompactionLifecycleKind]bool{}
	for i := range s.Facts {
		fact := s.Facts[i]
		var previous *ContextCompactionLifecycleFact
		if i > 0 {
			previous = &s.Facts[i-1]
		}
		if ValidateContextCompactionTransition(previous, fact) != nil || fact.OperationID != s.Start.OperationID || seen[fact.Kind] || fact.CreatedAt.Before(s.Start.StartedAt) {
			return ErrContextCompactionLifecycle
		}
		seen[fact.Kind] = true
	}
	latest := s.Facts[len(s.Facts)-1]
	if s.Status != latest.Kind {
		return ErrContextCompactionLifecycle
	}
	if s.TerminalAttempt != nil {
		if s.TerminalAttempt.Validate() != nil || s.TerminalAttempt.ID != s.Start.AttemptID || s.TerminalAttempt.TaskID != s.Start.TaskID ||
			s.TerminalAttempt.SourceSequence != s.Start.SourceSequence || s.TerminalAttempt.SourceDigest != s.Start.SourceDigest ||
			s.TerminalAttempt.Model != s.Start.Model || s.TerminalAttempt.Provider != s.Start.Provider || s.TerminalAttempt.Keep != s.Start.Keep ||
			s.TerminalAttempt.EstimatedCost != s.Start.EstimatedCost || !s.TerminalAttempt.StartedAt.Equal(s.Start.StartedAt) || s.TerminalAttempt.Status == "started" {
			return ErrContextCompactionLifecycle
		}
	}
	if s.Plan != nil {
		p := s.Plan
		if p.Validate() != nil || p.OperationID != s.Start.OperationID || p.OperationDigest != s.Start.OperationDigest ||
			p.RequestID != s.Start.RequestID || p.RequestDigest != s.Start.RequestDigest || p.ConfigDigest != s.Start.ConfigDigest ||
			p.PolicyDigest != s.Start.PolicyDigest || p.Engine.Digest != s.Start.Engine.Digest || p.Tiers.Digest != s.Start.Tiers.Digest ||
			p.Compaction.SourceTaskID != s.Start.TaskID || p.Compaction.SourceSequence != s.Start.SourceSequence ||
			p.Compaction.SourceDigest != s.Start.SourceDigest || p.Compaction.SummaryAttemptID != s.Start.AttemptID {
			return ErrContextCompactionLifecycle
		}
	}
	needsPlan := latest.Kind == ContextCompactionPrepared || latest.Kind == ContextCompactionValidated || latest.Kind == ContextCompactionApproved || latest.Kind == ContextCompactionRevoked || latest.Kind == ContextCompactionActivated || (latest.Kind == ContextCompactionFailed && latest.PlanDigest != "")
	if needsPlan != (s.Plan != nil) || (s.Plan != nil && (latest.PlanDigest != s.Plan.PlanDigest || s.TerminalAttempt == nil || s.TerminalAttempt.Status != "drafted")) {
		return ErrContextCompactionLifecycle
	}
	if s.Recovery != nil {
		if s.Recovery.Validate() != nil || s.Recovery.OperationID != s.Start.OperationID || s.Recovery.ProcessID == s.Start.ProcessID ||
			latest.Kind != ContextCompactionFailed || latest.Code != "owner_interrupted" || s.Recovery.FailedFactID != latest.ID || s.Recovery.FailedFactDigest != latest.Digest {
			return ErrContextCompactionLifecycle
		}
	}
	return nil
}

func canonicalLifecycleSnapshot(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(raw) == 0 || len(raw) > maxContextCompactionSnapshotBytes {
		return nil, "", ErrContextCompactionLifecycle
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeUniqueLifecycleJSON(decoder)
	if err != nil || value == nil {
		return nil, "", ErrContextCompactionLifecycle
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, "", ErrContextCompactionLifecycle
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > maxContextCompactionSnapshotBytes {
		return nil, "", ErrContextCompactionLifecycle
	}
	digest, err := lifecycleDigest(value)
	return json.RawMessage(canonical), digest, err
}

func decodeUniqueLifecycleJSON(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return token, nil
	}
	switch delim {
	case '{':
		value := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return nil, ErrContextCompactionLifecycle
			}
			if _, duplicate := value[key]; duplicate {
				return nil, ErrContextCompactionLifecycle
			}
			member, err := decodeUniqueLifecycleJSON(decoder)
			if err != nil {
				return nil, err
			}
			value[key] = member
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrContextCompactionLifecycle
		}
		return value, nil
	case '[':
		value := []any{}
		for decoder.More() {
			member, err := decodeUniqueLifecycleJSON(decoder)
			if err != nil {
				return nil, err
			}
			value = append(value, member)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrContextCompactionLifecycle
		}
		return value, nil
	default:
		return nil, ErrContextCompactionLifecycle
	}
}

func lifecycleDigest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > MaxContextCompactionLifecycleBytes {
		return "", ErrContextCompactionLifecycle
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func lifecycleSize(value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) > MaxContextCompactionLifecycleBytes {
		return ErrContextCompactionLifecycle
	}
	return nil
}

func cloneLifecycle[T any](value T) (T, error) {
	var out T
	body, err := json.Marshal(value)
	if err != nil || len(body) > MaxContextCompactionLifecycleBytes || json.Unmarshal(body, &out) != nil {
		return out, ErrContextCompactionLifecycle
	}
	return out, nil
}

func validLifecycleID(value string) bool {
	if len(value) < 1 || len(value) > 128 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) })
}

func validLifecycleCode(value string) bool {
	if len(value) < 1 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func validLifecycleDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
