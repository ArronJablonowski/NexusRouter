package skills

import "time"

// LearningActivationIntent pins the first activation precondition before the
// external catalog commit. It is not approval or evidence of completed activation.
type LearningActivationIntent struct {
	Version          int             `json:"version"`
	Scope            string          `json:"scope"`
	Name             string          `json:"name"`
	SelectionID      string          `json:"selection_id"`
	PolicyDigest     string          `json:"policy_digest"`
	ValidatorID      string          `json:"validator_id"`
	LearningRevision int64           `json:"learning_revision"`
	Expected         ActivationState `json:"expected"`
	Candidate        string          `json:"candidate"`
	CreatedAt        time.Time       `json:"created_at"`
}

func (i LearningActivationIntent) Validate() error {
	_, offset := i.CreatedAt.Zone()
	if i.Version != 1 || !identifier.MatchString(i.Scope) || !identifier.MatchString(i.Name) || !identifier.MatchString(i.ValidatorID) || !selectionDigest(i.SelectionID) || !selectionDigest(i.PolicyDigest) || i.LearningRevision < 1 || i.LearningRevision > 1_000_000_000 || i.Expected.Validate() != nil || i.Expected.Key.Scope != i.Scope || !selectionDigest(i.Expected.Key.Name) || !versionID(i.Candidate) || i.Candidate == i.Expected.Active || offset != 0 || i.CreatedAt.Year() < 1970 || i.CreatedAt.Year() >= 2261 {
		return ErrInvalid
	}
	return nil
}
