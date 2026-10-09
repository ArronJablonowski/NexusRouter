package routing

import (
	"testing"
	"time"
)

func TestAutomaticAccuracyOutranksLatencyCostAndLocality(t *testing.T) {
	r, candidates, now := fixture()
	evidence := map[Key]Evidence{
		{"local", "ollama", "code", "default"}: {Samples: 100, Quality: .85, Compliance: 1, Reliability: 1, Updated: now},
		{"cloud", "remote", "code", "default"}: {Samples: 100, Quality: .95, Compliance: 1, Reliability: 1, Latency: 10 * time.Minute, Cost: .9, Updated: now},
	}
	for _, remoteIsLocalInference := range []bool{false, true} {
		candidates[1].Local = remoteIsLocalInference
		selection, err := Select(r, Defaults(), candidates, evidence, now, .9)
		if err != nil || selection.Primary.Model != "cloud" {
			t.Fatalf("local inference %v: lower accuracy won: %+v, %v", remoteIsLocalInference, selection, err)
		}
	}
}

func TestOrdinaryRequestsNeverExploreAwayFromBestAccuracy(t *testing.T) {
	r, candidates, now := fixture()
	evidence := map[Key]Evidence{
		{"local", "ollama", "code", "default"}: {Samples: 100, Quality: 1, Compliance: 1, Reliability: 1, Updated: now},
		{"cloud", "remote", "code", "default"}: {Samples: 100, Quality: .2, Compliance: 1, Reliability: 1, Updated: now},
	}
	selection, err := Select(r, Defaults(), candidates, evidence, now, 0)
	if err != nil || selection.Explored || selection.Primary.Model != "local" {
		t.Fatalf("ordinary request explored a weaker model: %+v, %v", selection, err)
	}
}

func TestHistoricalWeightedRoutingPolicyRemainsReadable(t *testing.T) {
	r, candidates, now := fixture()
	policy := Defaults()
	policy.AccuracyFirst = false
	evidence := map[Key]Evidence{
		{"local", "ollama", "code", "default"}: {Samples: 100, Quality: .85, Compliance: 1, Reliability: 1, Updated: now},
		{"cloud", "remote", "code", "default"}: {Samples: 100, Quality: .95, Compliance: 1, Reliability: 1, Latency: 10 * time.Minute, Cost: .9, Updated: now},
	}
	selection, err := Select(r, policy, candidates, evidence, now, .9)
	if err != nil || selection.Primary.Model != "local" || ValidateExplanation(candidates, &policy, &selection) != nil {
		t.Fatalf("historical weighted policy changed: %v", err)
	}
}
