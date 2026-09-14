package releasepack

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

func TestFirstReleaseRollbackReadinessCanonicalChain(t *testing.T) {
	record, options := firstReleaseRollbackChainFixture(t)
	result, err := VerifyRollbackReadiness(t.Context(), options)
	if err != nil || result.RecordSHA256 != options.Expectations.RecordSHA256 || result.PublicationReceiptSHA256 != record.Current.PublicationReceiptSHA256 || result.RehearsalSHA256 != record.Rehearsal.EvidenceSHA256 {
		t.Fatal("canonical first-release chain rejected", result, err)
	}
	body, err := MarshalRollbackReadinessResult(result)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "rollback-readiness-verification.json")
	reservation, err := PrepareRollbackEvidenceOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	digest, err := reservation.CommitCanonical(body)
	if err != nil || digest != rollbackDigest(body) {
		t.Fatal("canonical verification receipt not retained", digest, err)
	}
	info, err := os.Lstat(out)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("verification receipt permissions", info, err)
	}
	retained, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRollbackReadinessResult(retained)
	if err != nil || parsed != result {
		t.Fatal("retained verification receipt rejected", parsed, err)
	}
	if _, err = PrepareRollbackEvidenceOutput(out); err == nil {
		t.Fatal("completed verification receipt was reused")
	}
}

func TestFirstReleaseRollbackReadinessCanonicalChainFailsClosed(t *testing.T) {
	t.Run("cross_chain_receipt_digest", func(t *testing.T) {
		record, options := firstReleaseRollbackChainFixture(t)
		evidence, err := VerifyPublishedInstallReceipt(options.RehearsalFile, options.Expectations.RehearsalSHA256)
		if err != nil {
			t.Fatal(err)
		}
		evidence.PublicationReceiptSHA256 = fingerprint("e")
		mismatched := filepath.Join(t.TempDir(), "mismatched-published-install.json")
		digest, err := WritePublishedInstallEvidence(mismatched, evidence)
		if err != nil {
			t.Fatal(err)
		}
		record.Rehearsal.EvidenceSHA256 = digest
		readiness := filepath.Join(t.TempDir(), "mismatched-readiness.json")
		recordDigest, err := WriteRollbackReadiness(readiness, record)
		if err != nil {
			t.Fatal(err)
		}
		options.RecordFile = readiness
		options.RehearsalFile = mismatched
		options.Expectations.RecordSHA256 = recordDigest
		options.Expectations.RehearsalSHA256 = digest
		if _, err = VerifyRollbackReadiness(t.Context(), options); err == nil {
			t.Fatal("published-install evidence from another receipt accepted")
		}
	})

	t.Run("verifier_role_collision", func(t *testing.T) {
		_, options := firstReleaseRollbackChainFixture(t)
		options.ReadinessVerifierID = options.Expectations.ReceiptVerifierID
		if _, err := VerifyRollbackReadiness(t.Context(), options); err == nil {
			t.Fatal("receipt observer reused as readiness verifier")
		}
	})
}

func firstReleaseRollbackChainFixture(t *testing.T) (RollbackReadiness, RollbackReadinessOptions) {
	t.Helper()
	receiptFile, installFile, installExpected := publishedInstallFixture(t)
	receiptBody, err := os.ReadFile(receiptFile)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ParsePostPublicationReceipt(receiptBody)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := CreatePublishedInstallEvidence(t.Context(), receiptFile, installFile, installExpected)
	if err != nil {
		t.Fatal(err)
	}
	rehearsalFile := filepath.Join(t.TempDir(), "published-install-evidence.json")
	rehearsalDigest, err := WritePublishedInstallEvidence(rehearsalFile, evidence, installExpected.DownloadDir, installExpected.InstallRoot)
	if err != nil {
		t.Fatal(err)
	}
	record := RollbackReadiness{
		SchemaVersion: 1,
		Project:       "DarwinRouter",
		Scope:         rollbackReadinessScope,
		Current: RollbackCurrentRelease{
			PublicationReceiptSHA256:       rollbackDigest(receiptBody),
			PublicationAuthorizationSHA256: receipt.PublicationAuthorizationSHA256,
			Repository:                     receipt.Repository,
			ReleaseVersion:                 receipt.ReleaseVersion,
			SourceCommit:                   receipt.SourceCommit,
			Tag:                            receipt.Tag,
			ReleaseID:                      receipt.ReleaseID,
			StateSchema:                    stateschema.Current,
		},
		History: RollbackHistoryPolicy{
			Mode:                 "first_release",
			FirstReleaseDecision: "approved_no_previous_public_release",
			FirstReleaseRollback: &FirstReleaseRollbackPolicy{
				DaemonAction: "stop",
				BinaryAction: "uninstall",
				DataAction:   "preserve_current_schema_no_restore",
			},
		},
		Rehearsal: RollbackRehearsal{
			EvidenceSHA256: rehearsalDigest,
			Scenario:       "published_native_install_rollback",
			FromSchema:     evidence.SourceSchema,
			ToSchema:       evidence.CurrentSchema,
			Status:         "passed",
			RehearsedAt:    evidence.VerifiedAt,
			VerifierID:     evidence.VerifierID,
		},
		Incident: RollbackIncident{OwnerID: "team:release-incident", StatusURL: "https://status.example.invalid/darwinrouter"},
		Approval: RollbackReadinessApproval{
			ApproverID: "idp:readiness-approver",
			PolicyURL:  "https://policy.example.invalid/rollback",
			ApprovedAt: "2026-09-07T01:03:00Z",
			ValidUntil: "2026-09-07T03:00:00Z",
		},
	}
	readinessFile := filepath.Join(t.TempDir(), "rollback-readiness.json")
	readinessDigest, err := WriteRollbackReadiness(readinessFile, record)
	if err != nil {
		t.Fatal(err)
	}
	expected := RollbackReadinessExpectations{
		RecordSHA256:                   readinessDigest,
		PublicationReceiptSHA256:       record.Current.PublicationReceiptSHA256,
		PublicationAuthorizationSHA256: record.Current.PublicationAuthorizationSHA256,
		Repository:                     record.Current.Repository,
		ReleaseVersion:                 record.Current.ReleaseVersion,
		SourceCommit:                   record.Current.SourceCommit,
		Tag:                            record.Current.Tag,
		ReleaseID:                      record.Current.ReleaseID,
		CurrentStateSchema:             record.Current.StateSchema,
		Mode:                           record.History.Mode,
		RehearsalSHA256:                record.Rehearsal.EvidenceSHA256,
		ReceiptVerifierID:              receipt.VerifierID,
		RehearsalVerifierID:            record.Rehearsal.VerifierID,
		IncidentOwnerID:                record.Incident.OwnerID,
		StatusURL:                      record.Incident.StatusURL,
		ApproverID:                     record.Approval.ApproverID,
		PolicyURL:                      record.Approval.PolicyURL,
		FirstReleaseDaemonAction:       record.History.FirstReleaseRollback.DaemonAction,
		FirstReleaseBinaryAction:       record.History.FirstReleaseRollback.BinaryAction,
		FirstReleaseDataAction:         record.History.FirstReleaseRollback.DataAction,
	}
	return record, RollbackReadinessOptions{
		RecordFile:          readinessFile,
		ReceiptFile:         receiptFile,
		RehearsalFile:       rehearsalFile,
		Expectations:        expected,
		ReceiptVerifier:     CanonicalPostPublicationReceiptVerifier{VerifierID: receipt.VerifierID},
		RehearsalVerifier:   CanonicalRollbackRehearsalVerifier{VerifierID: evidence.VerifierID},
		ReadinessVerifierID: "idp:readiness-verifier",
		Now: func() time.Time {
			return time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC)
		},
	}
}
