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
	Time                                      time.Time
}

func (r SummaryReview) Validate() error {
	for _, label := range []string{r.ID, r.AttemptID, r.PreviousID} {
		if len(label) > 128 || strings.TrimSpace(label) != label || !utf8.ValidString(label) || strings.ContainsFunc(label, unicode.IsControl) {
			return ErrHistory
		}
	}
	if r.Version != 1 || r.ID == "" || r.AttemptID == "" || r.PreviousID == r.ID || (r.Decision != "approved" && r.Decision != "rejected") || strings.TrimSpace(r.Note) == "" || len(r.Note) > 4096 || !utf8.ValidString(r.Note) || r.Time.IsZero() {
		return ErrHistory
	}
	return nil
}
