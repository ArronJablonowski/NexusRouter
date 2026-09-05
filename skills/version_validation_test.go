package skills

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func serializedVersionFixture() Version {
	return Version{ID: strings.Repeat("a", 32), CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Draft: sample()}
}

func TestVersionValidateManualAndGeneratedWithoutActivation(t *testing.T) {
	for _, generated := range []bool{false, true} {
		v := serializedVersionFixture()
		v.Draft.Tags = nil // Serialization has no domain-retrieval requirement.
		if generated {
			v.Parent = strings.Repeat("b", 32)
			v.Draft.SourceSessions = []string{"session-a", "session-b"}
			v.Draft.SourceEvidence = []string{"proof-a", "proof-b"}
		}
		before, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = v.Validate(); err != nil {
			t.Fatalf("valid generated=%v version rejected: %v", generated, err)
		}
		after, err := json.Marshal(v)
		if err != nil || string(before) != string(after) {
			t.Fatal("validation mutated input", err)
		}
	}
	for _, at := range []time.Time{time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2260, 12, 31, 23, 59, 59, 999999999, time.UTC)} {
		v := serializedVersionFixture()
		v.CreatedAt = at
		if err := v.Validate(); err != nil {
			t.Fatal("timestamp boundary rejected", at, err)
		}
	}
}

func TestVersionValidateRejectsMalformedAndOversizedRecords(t *testing.T) {
	for name, mutate := range map[string]func(*Version){
		"empty_id":          func(v *Version) { v.ID = "" },
		"short_id":          func(v *Version) { v.ID = "ab" },
		"uppercase_id":      func(v *Version) { v.ID = strings.Repeat("A", 32) },
		"nonhex_id":         func(v *Version) { v.ID = strings.Repeat("g", 32) },
		"huge_id":           func(v *Version) { v.ID = strings.Repeat("a", 1<<20) },
		"short_parent":      func(v *Version) { v.Parent = "ab" },
		"uppercase_parent":  func(v *Version) { v.Parent = strings.Repeat("B", 32) },
		"nonhex_parent":     func(v *Version) { v.Parent = strings.Repeat("x", 32) },
		"zero_time":         func(v *Version) { v.CreatedAt = time.Time{} },
		"old_time":          func(v *Version) { v.CreatedAt = time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC) },
		"future_time":       func(v *Version) { v.CreatedAt = time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC) },
		"nonutc_time":       func(v *Version) { v.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("offset", 3600)) },
		"nil_steps":         func(v *Version) { v.Draft.Steps = nil },
		"blank_steps":       func(v *Version) { v.Draft.Steps = []string{" "} },
		"no_sources":        func(v *Version) { v.Draft.SourceSessions = nil },
		"invalid_scope":     func(v *Version) { v.Draft.Key.Scope = "../scope" },
		"blank_description": func(v *Version) { v.Draft.Description = " " },
		"invalid_utf8":      func(v *Version) { v.Draft.Configuration = string([]byte{255}) },
		"huge_count": func(v *Version) {
			v.Draft.Risks = make([]string, 4097)
			for i := range v.Draft.Risks {
				v.Draft.Risks[i] = "risk"
			}
		},
		"huge_body":    func(v *Version) { v.Draft.Configuration = strings.Repeat("x", maxFile+1) },
		"draft_bytes":  func(v *Version) { v.Draft.Configuration = strings.Repeat("x", 256<<10) },
		"escaped_body": func(v *Version) { v.Draft.Configuration = strings.Repeat("\x00", 45000) },
	} {
		t.Run(name, func(t *testing.T) {
			v := serializedVersionFixture()
			mutate(&v)
			if err := v.Validate(); err != ErrInvalid {
				t.Fatalf("invalid version accepted or raw error exposed: %v", err)
			}
		})
	}
}
