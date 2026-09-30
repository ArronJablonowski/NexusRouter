package config

import "testing"

func TestCanonicalEnvironmentOverridesLegacyIndependentOfOrder(t *testing.T) {
	for _, env := range [][]string{
		{"NEXUS__MODE=local_only", "DARWIN__MODE=hybrid"},
		{"DARWIN__MODE=hybrid", "NEXUS__MODE=local_only"},
	} {
		if got := Environment(env)["mode"]; got != "local_only" {
			t.Fatal(got)
		}
	}
}
