package workboard

import (
	"strings"
	"testing"
	"time"
)

func auxiliaryReviewReservation() AuxiliaryReviewReservation {
	return AuxiliaryReviewReservation{Version: 1, BoardID: "board", CardID: "card", AttemptID: "attempt", ClaimID: "claim",
		CandidateID: "candidate", CandidateDigest: strings.Repeat("a", 64), CriteriaDigest: strings.Repeat("b", 64),
		PolicyDigest: strings.Repeat("c", 64), ReviewerID: "reviewer", ModelID: "review-model", ProviderID: "provider",
		ConfigID: strings.Repeat("d", 64), TimeLimitMS: 1_000, TokenLimit: 2_000, CostMicros: 3_000}
}

func auxiliaryReviewAdmission(t *testing.T) AuxiliaryReviewAdmissionRecord {
	t.Helper()
	r := auxiliaryReviewReservation()
	reservationDigest, err := r.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	a := AuxiliaryReviewAdmissionRecord{Version: 1, AdmissionID: "admission", OperationID: "operation",
		BoardID: r.BoardID, CardID: r.CardID, AttemptID: r.AttemptID, ClaimID: r.ClaimID, CandidateID: r.CandidateID,
		CandidateDigest: r.CandidateDigest, CriteriaDigest: r.CriteriaDigest, PolicyDigest: r.PolicyDigest,
		ReviewerID: r.ReviewerID, ModelID: r.ModelID, ProviderID: r.ProviderID, ConfigID: r.ConfigID,
		TimeLimitMS: r.TimeLimitMS, TokenLimit: r.TokenLimit, CostMicros: r.CostMicros,
		AdmittedAt: time.Unix(100, 0).UTC(), ReservationDigest: reservationDigest}
	a.AdmissionDigest, err = a.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func auxiliaryReviewSettlement(t *testing.T) AuxiliaryReviewSettlementRecord {
	t.Helper()
	a := auxiliaryReviewAdmission(t)
	s := AuxiliaryReviewSettlementRecord{Version: 1, SettlementID: "settlement", AdmissionID: a.AdmissionID,
		AdmissionDigest: a.AdmissionDigest, ReservationDigest: a.ReservationDigest, OperationID: a.OperationID,
		BoardID: a.BoardID, CardID: a.CardID, AttemptID: a.AttemptID, ClaimID: a.ClaimID, CandidateID: a.CandidateID,
		CandidateDigest: a.CandidateDigest, CriteriaDigest: a.CriteriaDigest, PolicyDigest: a.PolicyDigest,
		ReviewerID: a.ReviewerID, ModelID: a.ModelID, ProviderID: a.ProviderID, ConfigID: a.ConfigID,
		TimeLimitMS: a.TimeLimitMS, TokenLimit: a.TokenLimit, CostMicros: a.CostMicros, AdmittedAt: a.AdmittedAt,
		Disposition: AuxiliaryReviewCompleted, ChargedTimeMS: 500, ChargedTokens: a.TokenLimit,
		ChargedCostMicros: a.CostMicros, TimeChargeMode: AuxiliaryReviewMeasured,
		TokenChargeMode: AuxiliaryReviewConservative, CostChargeMode: AuxiliaryReviewConservative,
		SettledAt: a.AdmittedAt.Add(time.Second)}
	var err error
	s.SettlementDigest, err = s.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuxiliaryReviewReservationIsExplicitAndDigestBound(t *testing.T) {
	r := auxiliaryReviewReservation()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	digest, err := r.CanonicalDigest()
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	for name, mutate := range map[string]func(*AuxiliaryReviewReservation){
		"candidate": func(v *AuxiliaryReviewReservation) { v.CandidateDigest = strings.Repeat("e", 64) },
		"model":     func(v *AuxiliaryReviewReservation) { v.ModelID = "other" },
		"config":    func(v *AuxiliaryReviewReservation) { v.ConfigID = "invalid" },
		"time":      func(v *AuxiliaryReviewReservation) { v.TimeLimitMS = 0 },
		"tokens":    func(v *AuxiliaryReviewReservation) { v.TokenLimit = 0 },
		"cost":      func(v *AuxiliaryReviewReservation) { v.CostMicros = MaxWorkCostMicros + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := r
			mutate(&changed)
			if name == "candidate" || name == "model" {
				changedDigest, digestErr := changed.CanonicalDigest()
				if digestErr != nil || changedDigest == digest {
					t.Fatal("valid identity mutation did not alter digest")
				}
				return
			}
			if changed.Validate() == nil {
				t.Fatal("invalid reservation accepted")
			}
		})
	}
}

func TestAuxiliaryReviewAdmissionRequiresCanonicalReservationAndAdmissionDigests(t *testing.T) {
	a := auxiliaryReviewAdmission(t)
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*AuxiliaryReviewAdmissionRecord){
		"candidate":   func(v *AuxiliaryReviewAdmissionRecord) { v.CandidateID = "other" },
		"reservation": func(v *AuxiliaryReviewAdmissionRecord) { v.ReservationDigest = strings.Repeat("0", 64) },
		"admission":   func(v *AuxiliaryReviewAdmissionRecord) { v.AdmissionDigest = strings.Repeat("0", 64) },
		"timestamp": func(v *AuxiliaryReviewAdmissionRecord) {
			v.AdmittedAt = v.AdmittedAt.In(time.FixedZone("offset", 3600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := a
			mutate(&changed)
			if changed.Validate() == nil {
				t.Fatal("tampered admission accepted")
			}
		})
	}
}

func TestAuxiliaryReviewSettlementDistinguishesMeasuredAndConservativeCharges(t *testing.T) {
	s := auxiliaryReviewSettlement(t)
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	measuredOverrun := s
	measuredOverrun.ChargedTokens = s.TokenLimit + 1
	measuredOverrun.TokenChargeMode = AuxiliaryReviewMeasured
	measuredOverrun.SettlementDigest, _ = measuredOverrun.CanonicalDigest()
	if err := measuredOverrun.Validate(); err != nil {
		t.Fatal("measured overrun must remain recordable", err)
	}
	for name, mutate := range map[string]func(*AuxiliaryReviewSettlementRecord){
		"conservative_partial": func(v *AuxiliaryReviewSettlementRecord) { v.ChargedTokens-- },
		"unknown_mode":         func(v *AuxiliaryReviewSettlementRecord) { v.CostChargeMode = "unknown" },
		"disposition":          func(v *AuxiliaryReviewSettlementRecord) { v.Disposition = "started" },
		"before_admission":     func(v *AuxiliaryReviewSettlementRecord) { v.SettledAt = v.AdmittedAt.Add(-time.Nanosecond) },
		"admission_binding":    func(v *AuxiliaryReviewSettlementRecord) { v.ModelID = "forged" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := s
			mutate(&changed)
			if changed.Validate() == nil {
				t.Fatal("invalid settlement accepted")
			}
		})
	}
}
