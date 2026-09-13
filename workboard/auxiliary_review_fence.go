package workboard

import "time"

const AuxiliaryReviewSuccessorFenceVersion = 1

// AuxiliaryReviewSuccessorFence is the immutable, post-admission authority
// snapshot for a candidate review. Its presence distinguishes schema-44
// admissions from older accounting-only admissions, which are never
// backfilled into completion authority.
type AuxiliaryReviewSuccessorFence struct {
	Version          int       `json:"version"`
	AdmissionID      string    `json:"admission_id"`
	AdmissionDigest  string    `json:"admission_digest"`
	OperationID      string    `json:"operation_id"`
	BoardID          string    `json:"board_id"`
	CardID           string    `json:"card_id"`
	AttemptID        string    `json:"attempt_id"`
	ClaimID          string    `json:"claim_id"`
	CardRevision     int64     `json:"card_revision"`
	ClaimRevision    int64     `json:"claim_revision"`
	CriteriaRevision int64     `json:"criteria_revision"`
	CandidateID      string    `json:"candidate_id"`
	CandidateDigest  string    `json:"candidate_digest"`
	CriteriaDigest   string    `json:"criteria_digest"`
	PolicyDigest     string    `json:"policy_digest"`
	AdmittedAt       time.Time `json:"admitted_at"`
	DeadlineAt       time.Time `json:"deadline_at"`
	FenceDigest      string    `json:"fence_digest"`
}

func (f AuxiliaryReviewSuccessorFence) Validate() error {
	want, err := f.CanonicalDigest()
	if err != nil || f.Version != AuxiliaryReviewSuccessorFenceVersion ||
		!validLifecycleIDs(f.AdmissionID, f.OperationID, f.BoardID, f.CardID, f.AttemptID, f.ClaimID, f.CandidateID) ||
		!digest(f.AdmissionDigest) || f.CardRevision < 1 || f.ClaimRevision < 1 || f.CriteriaRevision < 1 ||
		!digest(f.CandidateDigest) || !digest(f.CriteriaDigest) || !digest(f.PolicyDigest) ||
		!validTime(f.AdmittedAt) || !validTime(f.DeadlineAt) || !f.DeadlineAt.After(f.AdmittedAt) ||
		f.DeadlineAt.Sub(f.AdmittedAt) > time.Duration(MaxAuxiliaryReviewDurationMillis)*time.Millisecond ||
		!digest(f.FenceDigest) || f.FenceDigest != want {
		return fail(CodeInvalid, "auxiliary_review_successor_fence")
	}
	return nil
}

func (f AuxiliaryReviewSuccessorFence) CanonicalDigest() (string, error) {
	f.FenceDigest = ""
	return executionDigest(f)
}
