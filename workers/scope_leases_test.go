package workers

import (
	"strings"
	"testing"
	"time"
)

func TestScopeLeaseStatusValidation(t *testing.T) {
	valid := func() ScopeLeaseStatus {
		return ScopeLeaseStatus{Version: 1, OverlapPolicyVersion: 1, Scope: "workspace", ObservedAt: time.Unix(100, 0).UTC(), StorageSchema: 23, Available: true, Holders: []ScopeLeaseHolder{{TaskID: "a", LiveReaders: 1}, {TaskID: "b", ExpiredWriters: 1}}}
	}
	if valid().Validate() != nil {
		t.Fatal("valid status rejected")
	}
	for name, mutate := range map[string]func(*ScopeLeaseStatus){
		"version": func(s *ScopeLeaseStatus) { s.Version = 2 }, "policy": func(s *ScopeLeaseStatus) { s.OverlapPolicyVersion = 2 },
		"scope": func(s *ScopeLeaseStatus) { s.Scope = " x" }, "nil": func(s *ScopeLeaseStatus) { s.Holders = nil },
		"schema": func(s *ScopeLeaseStatus) { s.StorageSchema = 29 }, "availability": func(s *ScopeLeaseStatus) { s.Available = false },
		"legacy": func(s *ScopeLeaseStatus) { s.StorageSchema = 2; s.Available = false }, "order": func(s *ScopeLeaseStatus) { s.Holders[0].TaskID = "z" },
		"duplicate": func(s *ScopeLeaseStatus) { s.Holders[1].TaskID = "a" }, "invalidtask": func(s *ScopeLeaseStatus) { s.Holders[0].TaskID = " x" },
		"zero": func(s *ScopeLeaseStatus) { s.Holders[0].LiveReaders = 0 }, "negative": func(s *ScopeLeaseStatus) { s.Holders[0].LiveReaders = -1 },
		"overflow": func(s *ScopeLeaseStatus) { s.Holders[0].LiveReaders = 1000 }, "year": func(s *ScopeLeaseStatus) { s.ObservedAt = time.Time{} },
		"zone": func(s *ScopeLeaseStatus) { s.ObservedAt = s.ObservedAt.In(time.FixedZone("offset", 3600)) },
	} {
		t.Run(name, func(t *testing.T) {
			s := valid()
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("accepted invalid status")
			}
		})
	}
	for _, schema := range []int{1, 2, 3, 23} {
		s := valid()
		s.StorageSchema = schema
		s.Available = schema >= 3
		s.Holders = []ScopeLeaseHolder{}
		if s.Validate() != nil {
			t.Fatal(schema)
		}
	}
	s := valid()
	s.Holders = []ScopeLeaseHolder{{TaskID: "a", LiveReaders: 1000}}
	if s.Validate() != nil {
		t.Fatal("exact limit")
	}
}

func TestValidLeaseScope(t *testing.T) {
	for _, s := range []string{"", " x", "x ", "a\nb", "\xff", strings.Repeat("x", 513)} {
		if ValidLeaseScope(s) {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"workspace", "create_a", "a/b", strings.Repeat("x", 512), "scope-世界"} {
		if !ValidLeaseScope(s) {
			t.Fatalf("rejected %q", s)
		}
	}
}
