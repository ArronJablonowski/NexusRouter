package sessions

import (
	"encoding/hex"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// SummaryRecovery is the redaction-safe receipt for conservatively closing a
// summary attempt after its exact local process owner is proven stopped.
// Process-lock metadata and generated output are intentionally excluded.
type SummaryRecovery struct {
	Version                             int
	ID, AttemptID, TaskID, SourceDigest string
	SourceSequence                      int64
	State, Code                         string
	RecoveredAt                         time.Time
}

func (r SummaryRecovery) Validate() error {
	for _, value := range []string{r.AttemptID, r.TaskID} {
		if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return ErrHistory
		}
	}
	if r.Version != 1 || len(r.ID) != 64 || strings.ToLower(r.ID) != r.ID || len(r.SourceDigest) != 64 || strings.ToLower(r.SourceDigest) != r.SourceDigest || r.SourceSequence < 1 || r.State != "interrupted" || r.Code != "owner_interrupted" || r.RecoveredAt.IsZero() || r.RecoveredAt.Year() < 1970 || r.RecoveredAt.Year() >= 2261 {
		return ErrHistory
	}
	if _, err := hex.DecodeString(r.ID); err != nil {
		return ErrHistory
	}
	if _, err := hex.DecodeString(r.SourceDigest); err != nil {
		return ErrHistory
	}
	return nil
}
