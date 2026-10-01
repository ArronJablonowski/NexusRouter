package v1_test

import (
	"os"
	"testing"
)

func TestSDKGoosePreservesContextAndEvidence(t *testing.T) {
	if os.Getenv("NEXUS_GOOSE_NATIVE") != "1" {
		t.Skip("native Goose required")
	}
	nativeSDKContextAndEvidence(t, "goose")
}
func TestSDKAutoLearnsGooseAndPi(t *testing.T) {
	if os.Getenv("NEXUS_GOOSE_NATIVE") != "1" || os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("native Goose and Pi required")
	}
	nativeSDKLearnsPair(t, "goose")
}
func TestSDKGooseAutomaticAuditFeedsAdvisoryEvidence(t *testing.T) {
	if os.Getenv("NEXUS_GOOSE_NATIVE") != "1" {
		t.Skip("native Goose required")
	}
	nativeAutomaticAudit(t, "goose")
}
