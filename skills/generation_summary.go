package skills

import (
	"encoding/hex"
	"strings"
	"time"
)

// GenerationSummary is an inspection projection with no workflow content or
// source session/evidence identifiers. HasResult means a saved proposal, not a
// published or validated skill. Started never implies safe redispatch.
type GenerationSummary struct {
	Version                                    int
	ID                                         string
	Key                                        Key
	Model, Provider, Status, Code, InputDigest string
	EstimatedCost                              float64
	StartedAt, FinishedAt                      time.Time
	HasResult                                  bool
}

func (s GenerationSummary) Validate() error {
	if s.Version != 1 || !identifier.MatchString(s.ID) || !s.Key.valid() || !generationLabel(s.Model) || !generationLabel(s.Provider) || len(s.InputDigest) != 64 || strings.ToLower(s.InputDigest) != s.InputDigest || !modelDraftCost(s.EstimatedCost) || !generationTime(s.StartedAt) {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(s.InputDigest); err != nil {
		return ErrInvalid
	}
	if s.HasResult != (s.Status == "drafted") {
		return ErrInvalid
	}
	switch s.Status {
	case "started":
		if !s.FinishedAt.IsZero() || s.Code != "" {
			return ErrInvalid
		}
	case "failed", "drafted":
		if !generationTime(s.FinishedAt) || s.FinishedAt.Before(s.StartedAt) {
			return ErrInvalid
		}
		if s.Status == "drafted" && s.Code != "" {
			return ErrInvalid
		}
		if s.Status == "failed" && s.Code != "generation_failed" && s.Code != "canceled" && s.Code != "persistence_failed" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func (a GenerationAttempt) Summary() GenerationSummary {
	return GenerationSummary{Version: a.Version, ID: a.ID, Key: a.Key, Model: a.Model, Provider: a.Provider, Status: a.Status, Code: a.Code, InputDigest: a.InputDigest, EstimatedCost: a.EstimatedCost, StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, HasResult: a.Result != nil}
}
