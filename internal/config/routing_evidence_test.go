package config

import "testing"

func TestRoutingEvidenceFallbacksAreExplicitAndExact(t *testing.T) {
	s, err := Load(Options{ProjectFile: file(t, `routing:
  evidence_fallbacks:
    - {domain: code, profile: default, source_domain: coding, source_profile: benchmark}
`)})
	if err != nil {
		t.Fatal(err)
	}
	d, p, ok := s.Routing.EvidenceSource("code", "default")
	if !ok || d != "coding" || p != "benchmark" {
		t.Fatalf("%s %s %v", d, p, ok)
	}
	for _, key := range [][2]string{{"code", "other"}, {"math", "default"}, {"coding", "benchmark"}} {
		if _, _, ok := s.Routing.EvidenceSource(key[0], key[1]); ok {
			t.Fatal("implicit mapping", key)
		}
	}
	valid := s.Routing.EvidenceFallbacks[0]
	for _, values := range [][]EvidenceFallback{{valid, valid}, {{Domain: "code", Profile: "default", SourceDomain: "code", SourceProfile: "default"}}, {{Domain: "code", Profile: "default", SourceProfile: "benchmark"}}} {
		s.Routing.EvidenceFallbacks = values
		if s.Validate() == nil {
			t.Fatal("accepted invalid fallback", values)
		}
	}
}
