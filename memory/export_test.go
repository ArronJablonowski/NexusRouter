package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestExportSnapshotValidation(t *testing.T) {
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	f := Fact{Version: 1, ID: "a", Scope: "scope", Revision: 1, Content: "fact", Provenance: "operator", Confidence: 1, Privacy: "local_only", Created: now, Updated: now}
	base := ExportSnapshot{Version: 1, Scope: "scope", CapturedAt: now, Facts: []Fact{f}}
	if base.Validate() != nil {
		t.Fatal("valid snapshot rejected")
	}
	empty := base
	empty.Facts = []Fact{}
	if empty.Validate() != nil {
		t.Fatal("empty scoped snapshot rejected")
	}
	for _, mutate := range []func(*ExportSnapshot){
		func(s *ExportSnapshot) { s.Version = 2 },
		func(s *ExportSnapshot) { s.Scope = "other" },
		func(s *ExportSnapshot) { s.CapturedAt = time.Time{} },
		func(s *ExportSnapshot) { s.Facts = nil },
		func(s *ExportSnapshot) { s.Facts = append(s.Facts, f) },
		func(s *ExportSnapshot) { s.Facts[0].Content = "" },
	} {
		bad := base
		bad.Facts = append([]Fact(nil), base.Facts...)
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
	full := base
	full.Facts = make([]Fact, ExportMaxFacts)
	for i := range full.Facts {
		full.Facts[i] = f
		full.Facts[i].ID = fmt.Sprintf("id-%04d", i)
	}
	if full.Validate() != nil {
		t.Fatal("exact count rejected")
	}
	full.Facts = append(full.Facts, f)
	if full.Validate() == nil {
		t.Fatal("over count accepted")
	}
	full.Facts = full.Facts[:200]
	for i := range full.Facts {
		full.Facts[i].Content = strings.Repeat("x", 65536)
	}
	if full.Validate() == nil {
		t.Fatal("oversized snapshot accepted")
	}
}
