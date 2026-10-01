package v1_test

import (
	"os"
	"testing"
)

func TestSDKOpenHandsPreservesContextAndEvidence(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" {
		t.Skip("native OpenHands required")
	}
	nativeSDKContextAndEvidence(t, "openhands")
}
func TestSDKAutoLearnsOpenHandsAndPi(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" || os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("native OpenHands and Pi required")
	}
	nativeSDKLearnsPair(t, "openhands")
}
func TestSDKOpenHandsAutomaticAuditFeedsAdvisoryEvidence(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" {
		t.Skip("native OpenHands required")
	}
	nativeAutomaticAudit(t, "openhands")
}
