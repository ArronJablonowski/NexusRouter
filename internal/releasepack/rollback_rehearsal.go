package releasepack

import "context"

// CanonicalRollbackRehearsalVerifier derives rehearsal identity exclusively
// from canonical published-install evidence. A caller cannot provide a
// free-standing "passed" assertion or choose the release identity.
type CanonicalRollbackRehearsalVerifier struct{ VerifierID string }

func (v CanonicalRollbackRehearsalVerifier) VerifyRehearsalEvidence(ctx context.Context, path, expectedSHA256 string) (RehearsalEvidenceIdentity, error) {
	var empty RehearsalEvidenceIdentity
	if ctx == nil || ctx.Err() != nil || !rollbackIdentity.MatchString(v.VerifierID) || !trustFingerprint(expectedSHA256) {
		return empty, ErrRollbackReadiness
	}
	evidence, err := VerifyPublishedInstallReceipt(path, expectedSHA256)
	if err != nil || evidence.VerifierID != v.VerifierID || ctx.Err() != nil {
		return empty, ErrRollbackReadiness
	}
	return RehearsalEvidenceIdentity{
		EvidenceSHA256: expectedSHA256, Scenario: "published_native_install_rollback",
		FromSchema: evidence.SourceSchema, ToSchema: evidence.CurrentSchema,
		Status: "passed", RehearsedAt: evidence.VerifiedAt, VerifierID: evidence.VerifierID,
		Repository: evidence.Repository, ReleaseVersion: evidence.ReleaseVersion,
		SourceCommit: evidence.SourceCommit, Tag: evidence.Tag, ReleaseID: evidence.ReleaseID,
		PublicationReceiptSHA256:       evidence.PublicationReceiptSHA256,
		PublicationAuthorizationSHA256: evidence.PublicationAuthorizationSHA256,
		InstallEvidenceSHA256:          evidence.InstallEvidenceSHA256, TargetOS: evidence.TargetOS,
		TargetArch: evidence.TargetArch, ArtifactName: evidence.ArtifactName,
		ArtifactSHA256: evidence.ArtifactSHA256, InstalledBinarySHA256: evidence.InstalledBinarySHA256,
		BackupSHA256: evidence.BackupSHA256, RollbackSchema: evidence.RollbackSchema,
	}, nil
}
