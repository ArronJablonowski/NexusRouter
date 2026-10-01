package v1_test

import (
	"os"
	"testing"
)

func TestSDKHermesPreservesContextAndEvidence(t *testing.T) {
	if os.Getenv("NEXUS_HERMES_NATIVE") != "1" {
		t.Skip("requires installed Hermes")
	}
	nativeSDKContextAndEvidence(t, "hermes")
}

func TestSDKAutoLearnsHermesAndPi(t *testing.T) {
	if os.Getenv("NEXUS_HERMES_NATIVE") != "1" || os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires Hermes and Pi")
	}
	nativeSDKLearnsPair(t, "hermes")
}
func TestSDKHermesAutomaticAuditFeedsAdvisoryEvidence(t *testing.T) {
	if os.Getenv("NEXUS_HERMES_NATIVE") != "1" {
		t.Skip("requires Hermes")
	}
	nativeAutomaticAudit(t, "hermes")
}
