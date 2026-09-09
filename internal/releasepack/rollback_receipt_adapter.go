package releasepack

import (
	"context"
)

// CanonicalPostPublicationReceiptVerifier adapts the canonical, independently
// observed post-publication receipt to rollback readiness. VerifierID is an
// independently supplied expectation and must match the identity bound into the
// canonical receipt.
type CanonicalPostPublicationReceiptVerifier struct {
	VerifierID string
}

func (v CanonicalPostPublicationReceiptVerifier) VerifyPublicationReceipt(ctx context.Context, path, expectedSHA256 string) (PublicationReceiptIdentity, error) {
	var empty PublicationReceiptIdentity
	if ctx == nil || ctx.Err() != nil || !rollbackIdentity.MatchString(v.VerifierID) || !trustFingerprint(expectedSHA256) {
		return empty, ErrRollbackReadiness
	}
	body, err := readRollbackFile(path, 64<<10)
	if err != nil || rollbackDigest(body) != expectedSHA256 {
		return empty, ErrRollbackReadiness
	}
	receipt, err := ParsePostPublicationReceipt(body)
	if err != nil || ctx.Err() != nil || receipt.VerifierID != v.VerifierID {
		return empty, ErrRollbackReadiness
	}
	publishedAt, err := strictRollbackTime(receipt.PublishedAt)
	if err != nil {
		return empty, ErrRollbackReadiness
	}
	observedAt, err := strictRollbackTime(receipt.ObservedAt)
	if err != nil || observedAt.Before(publishedAt) {
		return empty, ErrRollbackReadiness
	}
	return PublicationReceiptIdentity{
		ReceiptSHA256: expectedSHA256, Repository: receipt.Repository,
		ReleaseVersion: receipt.ReleaseVersion, Tag: receipt.Tag,
		SourceCommit:                   receipt.SourceCommit,
		PublicationAuthorizationSHA256: receipt.PublicationAuthorizationSHA256,
		ReleaseID:                      receipt.ReleaseID, VerifiedAt: receipt.ObservedAt,
		VerifierID: receipt.VerifierID,
	}, nil
}
