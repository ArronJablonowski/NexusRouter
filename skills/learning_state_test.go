package skills

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLearningStateValidation(t *testing.T) {
	initial := LearningState{Version: 1, Scope: "project", Name: "default", Domain: "general", PolicyDigest: strings.Repeat("a", 64), Revision: 1, Phase: "discover"}
	for _, phase := range []string{"discover", "consume", "generate"} {
		s := initial
		if phase != "discover" {
			s.Revision = 2
			s.Phase = phase
			s.ScanRevision = 1
			s.Epoch = 1
			if phase == "generate" {
				s.ConsumeRevision = 1
				s.BucketAfter = strings.Repeat("b", 64)
			}
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		body, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var got LearningState
		if json.Unmarshal(body, &got) != nil || got != s {
			t.Fatal("round trip changed state")
		}
	}
	for name, mutate := range map[string]func(*LearningState){
		"version":         func(s *LearningState) { s.Version = 2 },
		"scope":           func(s *LearningState) { s.Scope = "../scope" },
		"policy":          func(s *LearningState) { s.PolicyDigest = strings.Repeat("A", 64) },
		"revision":        func(s *LearningState) { s.Revision = 0 },
		"huge":            func(s *LearningState) { s.Revision = 1_000_000_001 },
		"negative":        func(s *LearningState) { s.ScanRevision = -1 },
		"consume ahead":   func(s *LearningState) { s.ConsumeRevision = 1 },
		"epoch ahead":     func(s *LearningState) { s.Epoch = 1 },
		"phase":           func(s *LearningState) { s.Phase = "activate" },
		"initial consume": func(s *LearningState) { s.Phase = "consume"; s.ScanRevision = 1; s.Epoch = 1 },
		"discover cursor": func(s *LearningState) { s.BucketAfter = strings.Repeat("a", 64) },
		"consume gap":     func(s *LearningState) { s.Revision = 2; s.Phase = "consume"; s.ScanRevision = 2; s.Epoch = 1 },
		"generate empty":  func(s *LearningState) { s.Revision = 2; s.Phase = "generate" },
		"bad cursor": func(s *LearningState) {
			s.Revision = 2
			s.Phase = "generate"
			s.ScanRevision = 1
			s.ConsumeRevision = 1
			s.Epoch = 1
			s.BucketAfter = "bad"
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := initial
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("invalid state accepted")
			}
		})
	}
}
