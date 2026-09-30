package workboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

const (
	ExecutionReservationVersion = 1
	MaxExecutionWIPLimit        = 1024
	MaxExecutionModelBytes      = 512
)

// ExecutionReservation is the bounded, configuration-bound capacity requested
// by a trusted runtime host before it starts a Workboard task. Resource values
// are integer accounting units; zero means that resource is unbounded by the
// card contract. WIP limits are always explicit.
type ExecutionReservation struct {
	Version        int    `json:"version"`
	ModelID        string `json:"model_id"`
	ProviderID     string `json:"provider_id"`
	ConfigID       string `json:"config_id"`
	TimeLimitMS    int64  `json:"time_limit_ms"`
	TokenLimit     int64  `json:"token_limit"`
	CostMicros     int64  `json:"cost_micros"`
	GlobalWIPLimit int    `json:"global_wip_limit"`
	BoardWIPLimit  int    `json:"board_wip_limit"`
}

// Validate binds a reservation to the exact first runtime event. Admission
// code additionally compares these bounds with the card's frozen budget.
func (r ExecutionReservation) Validate(event runtime.Event) error {
	if r.Version != ExecutionReservationVersion || event.Validate() != nil || event.Kind != runtime.TaskStarted || event.Sequence != 1 ||
		!boundedText(r.ModelID, MaxExecutionModelBytes, false) || !validID(r.ProviderID) || !digest(r.ConfigID) ||
		event.Data.ModelID != r.ModelID || event.Data.ProviderID != r.ProviderID || event.Data.ConfigID != r.ConfigID ||
		r.TimeLimitMS < 0 || r.TimeLimitMS > MaxWorkDurationMillis || r.TokenLimit < 0 || r.TokenLimit > MaxWorkTokens ||
		r.CostMicros < 0 || r.CostMicros > MaxWorkCostMicros || !validExecutionWIP(r.GlobalWIPLimit) || !validExecutionWIP(r.BoardWIPLimit) ||
		r.BoardWIPLimit > r.GlobalWIPLimit {
		return fail(CodeInvalid, "execution_reservation")
	}
	return nil
}

func (r ExecutionReservation) CanonicalDigest() (string, error) {
	if r.Version != ExecutionReservationVersion {
		return "", fail(CodeInvalid, "execution_reservation")
	}
	return executionDigest(r)
}

// ExecutionAdmissionRecord is the immutable authority for one admitted task.
// Normalized database columns are indexes only; this canonical body and digest
// bind every runtime, Workboard, route, policy, and capacity identity.
type ExecutionAdmissionRecord struct {
	Version         int       `json:"version"`
	AdmissionID     string    `json:"admission_id"`
	TaskID          string    `json:"task_id"`
	SessionID       string    `json:"session_id"`
	EventID         string    `json:"event_id"`
	EventDigest     string    `json:"event_digest"`
	BoardID         string    `json:"board_id"`
	CardID          string    `json:"card_id"`
	AttemptID       string    `json:"attempt_id"`
	ClaimID         string    `json:"claim_id"`
	WorkerID        string    `json:"worker_id"`
	OperationID     string    `json:"operation_id"`
	RequestDigest   string    `json:"request_digest"`
	CardRevision    int64     `json:"card_revision"`
	ModelID         string    `json:"model_id"`
	ProviderID      string    `json:"provider_id"`
	ConfigID        string    `json:"config_id"`
	PolicyDigest    string    `json:"policy_digest"`
	TimeLimitMS     int64     `json:"time_limit_ms"`
	TokenLimit      int64     `json:"token_limit"`
	CostMicros      int64     `json:"cost_micros"`
	GlobalWIPLimit  int       `json:"global_wip_limit"`
	BoardWIPLimit   int       `json:"board_wip_limit"`
	AdmittedAt      time.Time `json:"admitted_at"`
	AdmissionDigest string    `json:"admission_digest"`
}

func (r ExecutionAdmissionRecord) Validate() error {
	want, err := r.CanonicalDigest()
	if err != nil || r.Version != SchemaVersion ||
		!validLifecycleIDs(r.AdmissionID, r.TaskID, r.SessionID, r.EventID, r.BoardID, r.CardID, r.AttemptID, r.ClaimID, r.WorkerID, r.OperationID) ||
		!digest(r.EventDigest) || !digest(r.RequestDigest) || !digest(r.ConfigID) || !digest(r.PolicyDigest) ||
		!boundedText(r.ModelID, MaxExecutionModelBytes, false) || !validID(r.ProviderID) ||
		r.CardRevision < 1 ||
		r.TimeLimitMS < 0 || r.TimeLimitMS > MaxWorkDurationMillis || r.TokenLimit < 0 || r.TokenLimit > MaxWorkTokens ||
		r.CostMicros < 0 || r.CostMicros > MaxWorkCostMicros || !validExecutionWIP(r.GlobalWIPLimit) || !validExecutionWIP(r.BoardWIPLimit) ||
		r.BoardWIPLimit > r.GlobalWIPLimit || !validTime(r.AdmittedAt) || !digest(r.AdmissionDigest) || r.AdmissionDigest != want {
		return fail(CodeInvalid, "execution_admission_record")
	}
	return nil
}

func (r ExecutionAdmissionRecord) CanonicalDigest() (string, error) {
	r.AdmissionDigest = ""
	return executionDigest(r)
}

// ExecutionSettlementRecord is the immutable terminal accounting fact for an
// admission. Unknown provider token usage is represented explicitly so the
// settlement writer can conservatively charge the reserved token amount.
type ExecutionSettlementRecord struct {
	Version             int          `json:"version"`
	SettlementID        string       `json:"settlement_id"`
	AdmissionID         string       `json:"admission_id"`
	AdmissionDigest     string       `json:"admission_digest"`
	TaskID              string       `json:"task_id"`
	SessionID           string       `json:"session_id"`
	BoardID             string       `json:"board_id"`
	CardID              string       `json:"card_id"`
	AttemptID           string       `json:"attempt_id"`
	ClaimID             string       `json:"claim_id"`
	WorkerID            string       `json:"worker_id"`
	OperationID         string       `json:"operation_id"`
	ModelID             string       `json:"model_id"`
	ProviderID          string       `json:"provider_id"`
	ConfigID            string       `json:"config_id"`
	TerminalEventID     string       `json:"terminal_event_id"`
	TerminalEventDigest string       `json:"terminal_event_digest"`
	TerminalKind        runtime.Kind `json:"terminal_kind"`
	TerminalSequence    int64        `json:"terminal_sequence"`
	ChargedTimeMS       int64        `json:"charged_time_ms"`
	ChargedTokens       int64        `json:"charged_tokens"`
	ChargedCostMicros   int64        `json:"charged_cost_micros"`
	TokenUsageKnown     bool         `json:"token_usage_known"`
	SettledAt           time.Time    `json:"settled_at"`
	SettlementDigest    string       `json:"settlement_digest"`
}

func (r ExecutionSettlementRecord) Validate() error {
	want, err := r.CanonicalDigest()
	if err != nil || r.Version != SchemaVersion ||
		!validLifecycleIDs(r.SettlementID, r.AdmissionID, r.TaskID, r.SessionID, r.BoardID, r.CardID, r.AttemptID, r.ClaimID, r.WorkerID, r.OperationID, r.TerminalEventID) ||
		!digest(r.AdmissionDigest) || !digest(r.ConfigID) || !digest(r.TerminalEventDigest) ||
		!boundedText(r.ModelID, MaxExecutionModelBytes, false) || !validID(r.ProviderID) || !terminalRuntimeKind(r.TerminalKind) || r.TerminalSequence < 2 ||
		r.ChargedTimeMS < 0 || r.ChargedTimeMS > MaxWorkDurationMillis || r.ChargedTokens < 0 || r.ChargedTokens > MaxWorkTokens ||
		r.ChargedCostMicros < 0 || r.ChargedCostMicros > MaxWorkCostMicros || !validTime(r.SettledAt) ||
		!digest(r.SettlementDigest) || r.SettlementDigest != want {
		return fail(CodeInvalid, "execution_settlement_record")
	}
	return nil
}

func (r ExecutionSettlementRecord) CanonicalDigest() (string, error) {
	r.SettlementDigest = ""
	return executionDigest(r)
}

func validExecutionWIP(value int) bool { return value >= 1 && value <= MaxExecutionWIPLimit }

func terminalRuntimeKind(kind runtime.Kind) bool {
	return kind == runtime.TaskCompleted || kind == runtime.TaskFailed || kind == runtime.TaskCanceled
}

func executionDigest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", fail(CodeInvalid, "execution_digest")
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
