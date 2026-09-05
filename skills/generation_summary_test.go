package skills

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGenerationSummaryProjectionAndValidation(t *testing.T) {
	for _, status := range []string{"started", "drafted", "failed"} {
		a := generationAttemptFixture(status)
		if a.Result != nil {
			a.Result.Draft.Description = "private-generated-workflow"
		}
		s := a.Summary()
		if s.Validate() != nil || s.ID != a.ID || s.HasResult != (status == "drafted") {
			t.Fatal(s)
		}
		body, err := json.Marshal(s)
		if err != nil || strings.Contains(string(body), "private-generated-workflow") || strings.Contains(string(body), "session-a") || strings.Contains(string(body), "proof-a") {
			t.Fatal("summary contains proposal/source content")
		}
	}
}

func TestGenerationSummaryRejectsInconsistentMetadata(t *testing.T) {
	for _, mutate := range []func(*GenerationSummary){
		func(s *GenerationSummary) { s.Version = 2 },
		func(s *GenerationSummary) { s.Key.Scope = "../scope" },
		func(s *GenerationSummary) { s.ID = "" },
		func(s *GenerationSummary) { s.InputDigest = "invalid" },
		func(s *GenerationSummary) { s.InputDigest = strings.Repeat("A", 64) },
		func(s *GenerationSummary) { s.EstimatedCost = -1 },
		func(s *GenerationSummary) { s.HasResult = false },
		func(s *GenerationSummary) { s.Status = "active" },
		func(s *GenerationSummary) { s.FinishedAt = time.Time{} },
		func(s *GenerationSummary) { s.FinishedAt = s.StartedAt.Add(-time.Second) },
		func(s *GenerationSummary) { s.Code = "private-error" },
	} {
		s := generationAttemptFixture("drafted").Summary()
		mutate(&s)
		if s.Validate() == nil {
			t.Fatal("invalid summary admitted", s)
		}
	}
	for _, status := range []string{"started", "failed"} {
		s := generationAttemptFixture(status).Summary()
		s.HasResult = true
		if s.Validate() == nil {
			t.Fatal("non-draft has proposal")
		}
	}
}
