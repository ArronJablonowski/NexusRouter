package sessions

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type SummaryReview struct {
	Version                                   int
	ID, AttemptID, PreviousID, Decision, Note string
	ValidatorID, SourceDigest, DraftDigest    string
	SourceSequence                            int64
	Time                                      time.Time
}

func (r SummaryReview) Validate() error {
	for _, label := range []string{r.ID, r.AttemptID, r.PreviousID} {
		if len(label) > 128 || strings.TrimSpace(label) != label || !utf8.ValidString(label) || strings.ContainsFunc(label, unicode.IsControl) {
			return ErrHistory
		}
	}
	if (r.Version != 1 && r.Version != 2) || r.ID == "" || r.AttemptID == "" || r.PreviousID == r.ID || (r.Decision != "approved" && r.Decision != "rejected" && (r.Version != 2 || r.Decision != "abstained")) || strings.TrimSpace(r.Note) == "" || len(r.Note) > 4096 || !utf8.ValidString(r.Note) || r.Time.IsZero() {
		return ErrHistory
	}
	if r.Version == 1 {
		if r.ValidatorID != "" || r.SourceDigest != "" || r.DraftDigest != "" || r.SourceSequence != 0 {
			return ErrHistory
		}
		return nil
	}
	if !summaryValidatorID.MatchString(r.ValidatorID) || r.SourceSequence < 1 || len(r.SourceDigest) != 64 || len(r.DraftDigest) != 64 {
		return ErrHistory
	}
	for _, digest := range []string{r.SourceDigest, r.DraftDigest} {
		if strings.ToLower(digest) != digest {
			return ErrHistory
		}
		for _, c := range digest {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return ErrHistory
			}
		}
	}
	return nil
}
