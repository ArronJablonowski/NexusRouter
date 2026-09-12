package workboard

import "time"

const (
	AuxiliaryReviewReservationVersion = 1
	MinAuxiliaryReviewDurationMillis  = int64(100)
	MaxAuxiliaryReviewDurationMillis  = int64(5 * 60 * 1000)
)

// AuxiliaryReviewReservation is the complete card-owned capacity requested for
// one advisory candidate review. Time and token ceilings are explicit because
// an auxiliary call may never be admitted as unbounded. A zero cost ceiling is
// valid for a configured zero-cost reviewer.
type AuxiliaryReviewReservation struct {
	Version         int    `json:"version"`
	BoardID         string `json:"board_id"`
	CardID          string `json:"card_id"`
	AttemptID       string `json:"attempt_id"`
	ClaimID         string `json:"claim_id"`
	CandidateID     string `json:"candidate_id"`
	CandidateDigest string `json:"candidate_digest"`
	CriteriaDigest  string `json:"criteria_digest"`
	PolicyDigest    string `json:"policy_digest"`
	ReviewerID      string `json:"reviewer_id"`
	ModelID         string `json:"model_id"`
	ProviderID      string `json:"provider_id"`
	ConfigID        string `json:"config_id"`
	TimeLimitMS     int64  `json:"time_limit_ms"`
	TokenLimit      int64  `json:"token_limit"`
	CostMicros      int64  `json:"cost_micros"`
}

func (r AuxiliaryReviewReservation) Validate() error {
	if r.Version != AuxiliaryReviewReservationVersion ||
		!validLifecycleIDs(r.BoardID, r.CardID, r.AttemptID, r.ClaimID, r.CandidateID, r.ReviewerID) ||
		!digest(r.CandidateDigest) || !digest(r.CriteriaDigest) || !digest(r.PolicyDigest) || !digest(r.ConfigID) ||
		!boundedText(r.ModelID, MaxExecutionModelBytes, false) || !validID(r.ProviderID) ||
		r.TimeLimitMS < MinAuxiliaryReviewDurationMillis || r.TimeLimitMS > MaxAuxiliaryReviewDurationMillis ||
		r.TokenLimit < 1 || r.TokenLimit > MaxWorkTokens ||
		r.CostMicros < 0 || r.CostMicros > MaxWorkCostMicros {
		return fail(CodeInvalid, "auxiliary_review_reservation")
	}
	return nil
}

func (r AuxiliaryReviewReservation) CanonicalDigest() (string, error) {
	if r.Validate() != nil {
		return "", fail(CodeInvalid, "auxiliary_review_reservation")
	}
	return executionDigest(r)
}

// AuxiliaryReviewAdmissionRecord is the immutable authority for one admitted
// review. ReservationDigest binds the independently validated request, while
// AdmissionDigest binds the assigned operation identity and admission time.
type AuxiliaryReviewAdmissionRecord struct {
	Version           int       `json:"version"`
	AdmissionID       string    `json:"admission_id"`
	OperationID       string    `json:"operation_id"`
	BoardID           string    `json:"board_id"`
	CardID            string    `json:"card_id"`
	AttemptID         string    `json:"attempt_id"`
	ClaimID           string    `json:"claim_id"`
	CandidateID       string    `json:"candidate_id"`
	CandidateDigest   string    `json:"candidate_digest"`
	CriteriaDigest    string    `json:"criteria_digest"`
	PolicyDigest      string    `json:"policy_digest"`
	ReviewerID        string    `json:"reviewer_id"`
	ModelID           string    `json:"model_id"`
	ProviderID        string    `json:"provider_id"`
	ConfigID          string    `json:"config_id"`
	TimeLimitMS       int64     `json:"time_limit_ms"`
	TokenLimit        int64     `json:"token_limit"`
	CostMicros        int64     `json:"cost_micros"`
	AdmittedAt        time.Time `json:"admitted_at"`
	ReservationDigest string    `json:"reservation_digest"`
	AdmissionDigest   string    `json:"admission_digest"`
}

func (r AuxiliaryReviewAdmissionRecord) reservation() AuxiliaryReviewReservation {
	return AuxiliaryReviewReservation{Version: AuxiliaryReviewReservationVersion, BoardID: r.BoardID, CardID: r.CardID,
		AttemptID: r.AttemptID, ClaimID: r.ClaimID, CandidateID: r.CandidateID, CandidateDigest: r.CandidateDigest,
		CriteriaDigest: r.CriteriaDigest, PolicyDigest: r.PolicyDigest, ReviewerID: r.ReviewerID, ModelID: r.ModelID,
		ProviderID: r.ProviderID, ConfigID: r.ConfigID, TimeLimitMS: r.TimeLimitMS, TokenLimit: r.TokenLimit, CostMicros: r.CostMicros}
}

func (r AuxiliaryReviewAdmissionRecord) Validate() error {
	reservationDigest, reservationErr := r.reservation().CanonicalDigest()
	want, err := r.CanonicalDigest()
	if err != nil || reservationErr != nil || r.Version != SchemaVersion ||
		!validLifecycleIDs(r.AdmissionID, r.OperationID) || !validTime(r.AdmittedAt) ||
		!digest(r.ReservationDigest) || r.ReservationDigest != reservationDigest ||
		!digest(r.AdmissionDigest) || r.AdmissionDigest != want {
		return fail(CodeInvalid, "auxiliary_review_admission_record")
	}
	return nil
}

func (r AuxiliaryReviewAdmissionRecord) CanonicalDigest() (string, error) {
	r.AdmissionDigest = ""
	return executionDigest(r)
}

type AuxiliaryReviewDisposition string

const (
	AuxiliaryReviewCompleted AuxiliaryReviewDisposition = "completed"
	AuxiliaryReviewFailed    AuxiliaryReviewDisposition = "failed"
	AuxiliaryReviewCanceled  AuxiliaryReviewDisposition = "canceled"
)

type AuxiliaryReviewChargeMode string

const (
	// AuxiliaryReviewMeasured means the charged quantity came from a trusted
	// terminal measurement and may expose an overrun of the reservation.
	AuxiliaryReviewMeasured AuxiliaryReviewChargeMode = "measured"
	// AuxiliaryReviewConservative means measurement was unavailable and the
	// entire reserved quantity was charged.
	AuxiliaryReviewConservative AuxiliaryReviewChargeMode = "conservative"
)

// AuxiliaryReviewSettlementRecord is the immutable terminal accounting fact
// for an auxiliary review. Each resource states whether its charge was measured
// or conservatively substituted from the reservation.
type AuxiliaryReviewSettlementRecord struct {
	Version           int                        `json:"version"`
	SettlementID      string                     `json:"settlement_id"`
	AdmissionID       string                     `json:"admission_id"`
	AdmissionDigest   string                     `json:"admission_digest"`
	ReservationDigest string                     `json:"reservation_digest"`
	OperationID       string                     `json:"operation_id"`
	BoardID           string                     `json:"board_id"`
	CardID            string                     `json:"card_id"`
	AttemptID         string                     `json:"attempt_id"`
	ClaimID           string                     `json:"claim_id"`
	CandidateID       string                     `json:"candidate_id"`
	CandidateDigest   string                     `json:"candidate_digest"`
	CriteriaDigest    string                     `json:"criteria_digest"`
	PolicyDigest      string                     `json:"policy_digest"`
	ReviewerID        string                     `json:"reviewer_id"`
	ModelID           string                     `json:"model_id"`
	ProviderID        string                     `json:"provider_id"`
	ConfigID          string                     `json:"config_id"`
	TimeLimitMS       int64                      `json:"time_limit_ms"`
	TokenLimit        int64                      `json:"token_limit"`
	CostMicros        int64                      `json:"cost_micros"`
	AdmittedAt        time.Time                  `json:"admitted_at"`
	Disposition       AuxiliaryReviewDisposition `json:"disposition"`
	ChargedTimeMS     int64                      `json:"charged_time_ms"`
	ChargedTokens     int64                      `json:"charged_tokens"`
	ChargedCostMicros int64                      `json:"charged_cost_micros"`
	TimeChargeMode    AuxiliaryReviewChargeMode  `json:"time_charge_mode"`
	TokenChargeMode   AuxiliaryReviewChargeMode  `json:"token_charge_mode"`
	CostChargeMode    AuxiliaryReviewChargeMode  `json:"cost_charge_mode"`
	SettledAt         time.Time                  `json:"settled_at"`
	SettlementDigest  string                     `json:"settlement_digest"`
}

func (r AuxiliaryReviewSettlementRecord) admission() AuxiliaryReviewAdmissionRecord {
	return AuxiliaryReviewAdmissionRecord{Version: SchemaVersion, AdmissionID: r.AdmissionID, OperationID: r.OperationID,
		BoardID: r.BoardID, CardID: r.CardID, AttemptID: r.AttemptID, ClaimID: r.ClaimID, CandidateID: r.CandidateID,
		CandidateDigest: r.CandidateDigest, CriteriaDigest: r.CriteriaDigest, PolicyDigest: r.PolicyDigest,
		ReviewerID: r.ReviewerID, ModelID: r.ModelID, ProviderID: r.ProviderID, ConfigID: r.ConfigID,
		TimeLimitMS: r.TimeLimitMS, TokenLimit: r.TokenLimit, CostMicros: r.CostMicros, AdmittedAt: r.AdmittedAt,
		ReservationDigest: r.ReservationDigest, AdmissionDigest: r.AdmissionDigest}
}

func (r AuxiliaryReviewSettlementRecord) Validate() error {
	want, err := r.CanonicalDigest()
	if err != nil || r.Version != SchemaVersion || r.admission().Validate() != nil || !validID(r.SettlementID) ||
		(r.Disposition != AuxiliaryReviewCompleted && r.Disposition != AuxiliaryReviewFailed && r.Disposition != AuxiliaryReviewCanceled) ||
		r.ChargedTimeMS < 0 || r.ChargedTimeMS > MaxAuxiliaryReviewDurationMillis ||
		r.ChargedTokens < 0 || r.ChargedTokens > MaxWorkTokens ||
		r.ChargedCostMicros < 0 || r.ChargedCostMicros > MaxWorkCostMicros ||
		!validAuxiliaryReviewCharge(r.TimeChargeMode, r.ChargedTimeMS, r.TimeLimitMS) ||
		!validAuxiliaryReviewCharge(r.TokenChargeMode, r.ChargedTokens, r.TokenLimit) ||
		!validAuxiliaryReviewCharge(r.CostChargeMode, r.ChargedCostMicros, r.CostMicros) ||
		!validTime(r.SettledAt) || r.SettledAt.Before(r.AdmittedAt) ||
		!digest(r.SettlementDigest) || r.SettlementDigest != want {
		return fail(CodeInvalid, "auxiliary_review_settlement_record")
	}
	return nil
}

func (r AuxiliaryReviewSettlementRecord) CanonicalDigest() (string, error) {
	r.SettlementDigest = ""
	return executionDigest(r)
}

func validAuxiliaryReviewCharge(mode AuxiliaryReviewChargeMode, charged, reserved int64) bool {
	return mode == AuxiliaryReviewMeasured || mode == AuxiliaryReviewConservative && charged == reserved
}
