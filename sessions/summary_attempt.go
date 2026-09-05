package sessions

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// SummaryAttempt tracks a proposal, never activation of generated memory.
type SummaryAttempt struct {
	Version                                                 int
	ID, TaskID, SourceDigest, Model, Provider, Status, Code string
	SourceSequence                                          int64
	Keep                                                    int
	EstimatedCost                                           float64
	StartedAt, FinishedAt                                   time.Time
	Draft                                                   *SummaryDraft
}

func (a SummaryAttempt) Validate() error {
	for _, s := range []string{a.ID, a.TaskID, a.Model, a.Provider} {
		if s == "" || len(s) > 128 || strings.TrimSpace(s) != s || !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
			return ErrHistory
		}
	}
	if a.Version != 1 || a.SourceSequence < 1 || a.Keep < 1 || a.Keep > 100000 || len(a.SourceDigest) != 64 || strings.ToLower(a.SourceDigest) != a.SourceDigest || a.StartedAt.IsZero() || math.IsNaN(a.EstimatedCost) || math.IsInf(a.EstimatedCost, 0) || a.EstimatedCost < 0 {
		return ErrHistory
	}
	if _, err := hex.DecodeString(a.SourceDigest); err != nil {
		return ErrHistory
	}
	switch a.Status {
	case "started":
		if !a.FinishedAt.IsZero() || a.Draft != nil || a.Code != "" {
			return ErrHistory
		}
	case "failed":
		if a.FinishedAt.IsZero() || a.FinishedAt.Before(a.StartedAt) || a.Draft != nil || (a.Code != "summary_failed" && a.Code != "canceled" && a.Code != "persistence_failed") {
			return ErrHistory
		}
	case "drafted":
		if a.FinishedAt.IsZero() || a.FinishedAt.Before(a.StartedAt) || a.Code != "" || a.Draft == nil {
			return ErrHistory
		}
		d := a.Draft
		if ValidateCompactionRequest(&d.Request) != nil || d.Request.Keep != a.Keep || d.SourceTaskID != a.TaskID || d.SourceSequence != a.SourceSequence || d.SourceDigest != a.SourceDigest || d.Model != a.Model || d.Elapsed < 0 || d.Elapsed > time.Minute || d.Checkpoint == nil || d.Checkpoint.Validate(a.TaskID) != nil || d.Checkpoint.SourceSequence != a.SourceSequence || d.Checkpoint.SourceDigest != a.SourceDigest {
			return ErrHistory
		}
		if d.Usage != nil && (d.Usage.InputTokens < 0 || d.Usage.OutputTokens < 0) {
			return ErrHistory
		}
		requestSummary, err := json.Marshal(d.Request.Summary)
		if err != nil {
			return ErrHistory
		}
		checkpointSummary, err := json.Marshal(d.Checkpoint.Summary)
		if err != nil || !bytes.Equal(requestSummary, checkpointSummary) {
			return ErrHistory
		}
	default:
		return ErrHistory
	}
	return nil
}
