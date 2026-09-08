package config

import (
	"testing"
	"time"
)

func TestRoutingDecayOverridesAreExactAndLayered(t *testing.T) {
	s, err := Load(Options{ProjectFile: file(t, `routing:
  decay_half_life: 30d
  decay_overrides:
    - domain: coding
      profile: local
      half_life: 12h
`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		domain, profile string
		want            time.Duration
	}{{"coding", "local", 12 * time.Hour}, {"coding", "cloud", 30 * 24 * time.Hour}, {"creative", "local", 30 * 24 * time.Hour}} {
		got, err := s.Routing.DecayHalfLife(tc.domain, tc.profile)
		if err != nil || got != tc.want {
			t.Fatalf("DecayHalfLife(%q,%q) = %v, %v; want %v", tc.domain, tc.profile, got, err, tc.want)
		}
	}
}

func TestRoutingDecayOverridesRejectAmbiguityAndInvalidValues(t *testing.T) {
	for _, body := range []string{
		"routing:\n  decay_overrides:\n    - {domain: coding, profile: local, half_life: 1h}\n    - {domain: coding, profile: local, half_life: 2h}\n",
		"routing:\n  decay_overrides:\n    - {domain: '', profile: local, half_life: 1h}\n",
		"routing:\n  decay_overrides:\n    - {domain: coding, profile: local, half_life: 0s}\n",
	} {
		if _, err := Load(Options{ProjectFile: file(t, body)}); err == nil {
			t.Fatalf("accepted invalid decay override: %q", body)
		}
	}
}
