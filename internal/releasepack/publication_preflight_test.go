package releasepack

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationPreflightBindsVerifiedSignedSet(t *testing.T) {
	signing, _ := approvedSigningFixture(t)
	if err := SignApproved(context.Background(), signing); err != nil {
		t.Fatal(err)
	}
	verification := verificationOptions(signing)
	verified, err := VerifyApproved(context.Background(), verification)
	if err != nil {
		t.Fatal(err)
	}
	_, candidate, err := readCandidateRecord(signing.CandidateRecordFile)
	if err != nil {
		t.Fatal(err)
	}
	notesFile := filepath.Join(signing.Source, "docs", "release-notes.md")
	notes, err := os.ReadFile(notesFile)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := publicationAssets(signing.Dir)
	if err != nil {
		t.Fatal(err)
	}
	record := publicationAuthorizationFixture()
	record.ReleaseVersion, record.SourceCommit = candidate.ReleaseVersion, candidate.SourceCommit
	record.Tag, record.ReleaseTitle = "v"+candidate.ReleaseVersion, "DarwinRouter v"+candidate.ReleaseVersion
	record.ReleaseNotesSHA256 = publicationDigest(notes)
	record.CandidateRecordSHA256 = verified.CandidateRecordSHA256
	record.LicenseEvidenceSHA256 = verified.LicenseEvidenceSHA256
	record.SHA256SUMSSHA256 = verified.SHA256SUMSSHA256
	record.SignatureFileSHA256 = verified.SignatureFileSHA256
	record.TrustRecordSHA256 = verified.TrustRecordSHA256
	record.SigningAuthorizationSHA256 = verified.AuthorizationRecordSHA256
	record.Assets = assets
	body := canonicalPublicationAuthorization(t, record)
	recordFile := filepath.Join(t.TempDir(), "publication.json")
	if err = os.WriteFile(recordFile, body, 0644); err != nil {
		t.Fatal(err)
	}
	options := PublicationPreflightOptions{
		Verification: verification, PublicationAuthorizationFile: recordFile,
		ExpectedPublicationAuthorizationSHA256: publicationDigest(body),
		ExpectedRepository:                     record.Repository, ReleaseNotesFile: notesFile,
	}
	result, err := VerifyPublicationPreflight(context.Background(), options)
	if err != nil || result.Tag != record.Tag || result.SourceCommit != record.SourceCommit ||
		result.PublicationApproverID != record.ApproverID || !equalPublicationAssets(result.Assets, assets) {
		t.Fatal("preflight rejected", result, err)
	}

	for _, scenario := range []string{"record_digest", "repository", "notes", "asset", "signed_set", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			changed := options
			ctx := context.Background()
			switch scenario {
			case "record_digest":
				changed.ExpectedPublicationAuthorizationSHA256 = invalidPublicDigest()
			case "repository":
				changed.ExpectedRepository = "other/repository"
			case "notes":
				path := filepath.Join(t.TempDir(), "notes.md")
				if writeErr := os.WriteFile(path, []byte("different notes\n"), 0644); writeErr != nil {
					t.Fatal(writeErr)
				}
				changed.ReleaseNotesFile = path
			case "asset":
				tampered := clonePublicationAuthorization(record)
				tampered.Assets[0].SHA256 = invalidPublicDigest()
				tamperedBody := canonicalPublicationAuthorization(t, tampered)
				path := filepath.Join(t.TempDir(), "tampered.json")
				if writeErr := os.WriteFile(path, tamperedBody, 0644); writeErr != nil {
					t.Fatal(writeErr)
				}
				changed.PublicationAuthorizationFile = path
				changed.ExpectedPublicationAuthorizationSHA256 = publicationDigest(tamperedBody)
			case "signed_set":
				if writeErr := os.WriteFile(filepath.Join(signing.Dir, signatureName), []byte("tampered\n"), 0644); writeErr != nil {
					t.Fatal(writeErr)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if result, gotErr := VerifyPublicationPreflight(ctx, changed); gotErr == nil || result.PublicationAuthorizationSHA256 != "" || len(result.Assets) != 0 {
				t.Fatal("unsafe preflight accepted", result, gotErr)
			}
		})
	}
}
