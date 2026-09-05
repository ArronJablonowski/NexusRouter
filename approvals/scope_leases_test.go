package approvals

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestScopeLeaseObservationBoundsAndVersion(t *testing.T) {
	valid := ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1}
	for _, counts := range [][4]int{{0, 0, 0, 0}, {1, 2, 3, 4}, {1000, 0, 0, 0}, {0, 0, 0, 1000}} {
		s := valid
		s.LiveReaders, s.ExpiredReaders, s.LiveWriters, s.ExpiredWriters = counts[0], counts[1], counts[2], counts[3]
		if s.Validate() != nil {
			t.Fatal("valid observation rejected", s)
		}
	}
	for _, mutate := range []func(*ScopeLeaseObservation){
		func(s *ScopeLeaseObservation) { s.Version = 0 },
		func(s *ScopeLeaseObservation) { s.OverlapPolicyVersion = 2 },
		func(s *ScopeLeaseObservation) { s.LiveReaders = -1 },
		func(s *ScopeLeaseObservation) { s.ExpiredReaders = -1 },
		func(s *ScopeLeaseObservation) { s.LiveWriters = -1 },
		func(s *ScopeLeaseObservation) { s.ExpiredWriters = -1 },
		func(s *ScopeLeaseObservation) { s.LiveReaders = math.MaxInt },
		func(s *ScopeLeaseObservation) { s.LiveReaders = 500; s.ExpiredReaders = 501 },
		func(s *ScopeLeaseObservation) { s.LiveWriters = 999; s.ExpiredWriters = 2 },
	} {
		s := valid
		mutate(&s)
		if s.Validate() == nil {
			t.Fatal("invalid observation accepted", s)
		}
	}
}

func TestExecutionScopeLeasesOptionalAndConsistent(t *testing.T) {
	s := executionFixture()
	if s.Validate() != nil {
		t.Fatal("legacy status rejected")
	}
	b, err := json.Marshal(s)
	if err != nil || strings.Contains(string(b), "scope_leases") {
		t.Fatal("legacy unknown relabeled")
	}
	s.ScopeLeases = &ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1, ExpiredReaders: 1, LiveWriters: 2}
	// An exact-scope 'none' may still have overlapping legacy writers.
	if s.Validate() != nil {
		t.Fatal("overlap mistaken for exact scope")
	}
	s.ScopeWriterState = "live"
	if s.Validate() != nil {
		t.Fatal("live observation rejected")
	}
	s.ScopeWriterState = "expired"
	if s.Validate() == nil {
		t.Fatal("missing exact expired writer accepted")
	}
	s.ScopeLeases.ExpiredWriters = 1
	if s.Validate() != nil {
		t.Fatal("expired observation rejected")
	}
	s.ScopeLeases.OverlapPolicyVersion = 9
	if s.Validate() == nil {
		t.Fatal("unknown overlap policy accepted")
	}
}
