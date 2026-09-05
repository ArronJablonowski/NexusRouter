package skills

import (
	"strings"
	"testing"
)

func TestLearningStateTransitions(t *testing.T) {
	p := LearningState{Version: 1, Scope: "p", Name: "n", Domain: "d", PolicyDigest: strings.Repeat("a", 64), Revision: 4, Phase: "generate", ScanRevision: 1, ConsumeRevision: 1, Epoch: 1}
	for name, mutate := range map[string]func(*LearningState){
		"revision skip": func(s *LearningState) { s.Revision++ },
		"policy change": func(s *LearningState) { s.PolicyDigest = strings.Repeat("b", 64) },
		"domain change": func(s *LearningState) { s.Domain = "other" },
		"scan advance":  func(s *LearningState) { s.ScanRevision++; s.ConsumeRevision++ },
		"epoch advance": func(s *LearningState) { s.ScanRevision++; s.ConsumeRevision++; s.Epoch++ },
		"half pin":      func(s *LearningState) { s.PendingSelectionID = strings.Repeat("b", 64) },
		"bad pin":       func(s *LearningState) { s.PendingSelectionID = "bad"; s.PendingBucketID = strings.Repeat("c", 64) },
		"no progress":   func(s *LearningState) {},
	} {
		t.Run(name, func(t *testing.T) {
			s := p
			s.Revision++
			mutate(&s)
			if s.ValidateAfter(p) == nil {
				t.Fatal("accepted invalid transition")
			}
		})
	}
	pinned := p
	pinned.Revision++
	pinned.PendingSelectionID = strings.Repeat("b", 64)
	pinned.PendingBucketID = strings.Repeat("c", 64)
	if pinned.ValidateAfter(p) != nil {
		t.Fatal("pin rejected")
	}
	cleared := pinned
	cleared.Revision++
	cleared.PendingSelectionID = ""
	cleared.PendingBucketID = ""
	cleared.BucketAfter = pinned.PendingBucketID
	if cleared.ValidateAfter(pinned) != nil {
		t.Fatal("clear rejected")
	}
	bad := cleared
	bad.BucketAfter = strings.Repeat("d", 64)
	if bad.ValidateAfter(pinned) == nil {
		t.Fatal("skipped pinned bucket")
	}
	reset := cleared
	reset.Revision++
	reset.Phase = "discover"
	reset.BucketAfter = ""
	if reset.ValidateAfter(cleared) != nil {
		t.Fatal("reset rejected")
	}
	skip := cleared
	skip.Revision++
	skip.BucketAfter = strings.Repeat("d", 64)
	if skip.ValidateAfter(cleared) != nil {
		t.Fatal("singleton skip rejected")
	}
}
