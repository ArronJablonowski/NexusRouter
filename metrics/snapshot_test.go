package metrics

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestSnapshotSchemaAvailability(t *testing.T) {
	for schema := 1; schema <= 16; schema++ {
		s := NewSnapshot(schema, time.Now().UTC())
		if err := s.Validate(); err != nil {
			t.Fatal(schema, err)
		}
		body, err := json.Marshal(s)
		if err != nil || len(body) > 4096 {
			t.Fatal("unexpected payload size", len(body), err)
		}
		var decoded Snapshot
		if json.Unmarshal(body, &decoded) != nil || decoded.Validate() != nil {
			t.Fatal("invalid JSON round trip")
		}
		for i, g := range s.Groups {
			if g.Available != (schema >= definitions[i].since) {
				t.Fatal("wrong availability", g)
			}
		}
	}
}

func TestSnapshotRejectsInvalidOrUnboundedLabels(t *testing.T) {
	cases := map[string]func(*Snapshot){
		"version":              func(s *Snapshot) { s.Version++ },
		"future schema":        func(s *Snapshot) { s.StorageSchema = 17 },
		"missing schema":       func(s *Snapshot) { s.StorageSchema = 0 },
		"missing time":         func(s *Snapshot) { s.ObservedAt = time.Time{} },
		"unserializable time":  func(s *Snapshot) { s.ObservedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"invalid zone":         func(s *Snapshot) { s.ObservedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("", 25*60*60)) },
		"extra group":          func(s *Snapshot) { s.Groups = append(s.Groups, s.Groups[0]) },
		"missing group":        func(s *Snapshot) { s.Groups = s.Groups[1:] },
		"secret group":         func(s *Snapshot) { s.Groups[0].Name = "secret" },
		"secret state":         func(s *Snapshot) { s.Groups[0].Counts[0].State = "secret" },
		"availability":         func(s *Snapshot) { s.Groups[0].Available = false },
		"missing count":        func(s *Snapshot) { s.Groups[0].Counts = s.Groups[0].Counts[1:] },
		"negative":             func(s *Snapshot) { s.Groups[0].Counts[0].Value = -1 },
		"overflow":             func(s *Snapshot) { s.Groups[0].Counts[0].Value = math.MaxInt64; s.Groups[0].Counts[1].Value = 1 },
		"duplicate":            func(s *Snapshot) { s.Groups[0].Counts[1].State = "running" },
		"null unavailable":     func(s *Snapshot) { *s = NewSnapshot(1, s.ObservedAt); s.Groups[1].Counts = nil },
		"invented unavailable": func(s *Snapshot) { *s = NewSnapshot(1, s.ObservedAt); s.Groups[1].Counts = []Count{{State: "queued"}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := NewSnapshot(13, time.Now().UTC())
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}

func TestNewSnapshotDoesNotShareMutableState(t *testing.T) {
	a := NewSnapshot(13, time.Now())
	a.Groups[0].Counts[0].State = "changed"
	a.Groups[0].Counts[0].Value = 1
	b := NewSnapshot(13, time.Now())
	if b.Validate() != nil || b.Groups[0].Counts[0].Value != 0 {
		t.Fatal("shared mutable state")
	}
}
