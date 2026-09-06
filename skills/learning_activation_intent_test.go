package skills

import (
	"strings"
	"testing"
	"time"
)

func TestLearningActivationIntentValidation(t *testing.T) {
	base := LearningActivationIntent{Version: 1, Scope: "project", Name: "default", SelectionID: strings.Repeat("a", 64), PolicyDigest: strings.Repeat("b", 64), ValidatorID: "deterministic", LearningRevision: 2, Expected: ActivationState{Version: 1, Key: Key{Scope: "project", Name: strings.Repeat("c", 64)}, Revision: strings.Repeat("d", 64)}, Candidate: strings.Repeat("e", 32), CreatedAt: time.Unix(100, 0).UTC()}
	if base.Validate() != nil {
		t.Fatal("valid intent")
	}
	for name, mutate := range map[string]func(*LearningActivationIntent){
		"version": func(i *LearningActivationIntent) { i.Version = 2 }, "scope": func(i *LearningActivationIntent) { i.Scope = "other" }, "name": func(i *LearningActivationIntent) { i.Name = "" }, "validator": func(i *LearningActivationIntent) { i.ValidatorID = "" },
		"selection": func(i *LearningActivationIntent) { i.SelectionID = strings.Repeat("A", 64) }, "policy": func(i *LearningActivationIntent) { i.PolicyDigest = "short" }, "revision": func(i *LearningActivationIntent) { i.LearningRevision = 0 }, "revision-limit": func(i *LearningActivationIntent) { i.LearningRevision = 1_000_000_001 },
		"bucket": func(i *LearningActivationIntent) { i.Expected.Key.Name = "ordinary" }, "candidate": func(i *LearningActivationIntent) { i.Candidate = "bad" }, "active": func(i *LearningActivationIntent) { i.Expected.Active = i.Candidate }, "expected": func(i *LearningActivationIntent) { i.Expected.Revision = "" }, "time": func(i *LearningActivationIntent) { i.CreatedAt = time.Time{} }, "zone": func(i *LearningActivationIntent) { i.CreatedAt = i.CreatedAt.In(time.FixedZone("east", 3600)) },
	} {
		t.Run(name, func(t *testing.T) {
			i := base
			mutate(&i)
			if i.Validate() == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
}
