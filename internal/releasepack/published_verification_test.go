package releasepack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/githubverify"
)

func TestVerifyPublishedReleaseReverifiesFreshRemoteBytes(t *testing.T) {
	preflight, signedDir := publishedFixture(t)
	t.Run("valid", func(t *testing.T) {
		download := filepath.Join(t.TempDir(), "download")
		reader := &fixtureReleaseReader{source: signedDir}
		receipt, err := VerifyPublishedRelease(context.Background(), reader, PublishedVerificationOptions{Preflight: preflight, DownloadDir: download})
		if err != nil || receipt.Repository != preflight.ExpectedRepository || !receipt.Immutable || len(receipt.Assets) != 7 {
			t.Fatal("published release rejected", receipt, err)
		}
		body, err := MarshalPostPublicationReceipt(receipt)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParsePostPublicationReceipt(body)
		if err != nil || parsed.PublicationAuthorizationSHA256 != preflight.ExpectedPublicationAuthorizationSHA256 {
			t.Fatal("canonical receipt rejected", parsed, err)
		}
	})
	t.Run("replaced_download", func(t *testing.T) {
		reader := &fixtureReleaseReader{source: signedDir, corruptAfterCopy: true}
		result, err := VerifyPublishedRelease(context.Background(), reader, PublishedVerificationOptions{
			Preflight: preflight, DownloadDir: filepath.Join(t.TempDir(), "download"),
		})
		if err == nil || result.SchemaVersion != 0 || result.Repository != "" || len(result.Assets) != 0 {
			t.Fatal("replaced remote bytes accepted", result, err)
		}
	})
}

func TestPostPublicationReceiptRejectsNonGitHubEvidenceURL(t *testing.T) {
	if validGitHubReleaseURL("https://evil.example/acme/router/releases/tag/v1.0.0", "acme/router", "v1.0.0") {
		t.Fatal("non-GitHub receipt URL accepted")
	}
}

func TestPostPublicationReceiptRejectsCrossAuthorityAssetURL(t *testing.T) {
	preflight, signedDir := publishedFixture(t)
	receipt, err := VerifyPublishedRelease(context.Background(), &fixtureReleaseReader{source: signedDir}, PublishedVerificationOptions{
		Preflight: preflight, DownloadDir: filepath.Join(t.TempDir(), "download"),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Assets[0].DownloadURL = "https://github.com/other/repository/releases/download/" + receipt.Tag + "/" + receipt.Assets[0].Name
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	if _, err = ParsePostPublicationReceipt(body); err == nil {
		t.Fatal("cross-repository asset URL accepted by canonical receipt parser")
	}
}

func publishedFixture(t *testing.T) (PublicationPreflightOptions, string) {
	return publishedFixtureWithExecutable(t, false)
}

func publishedExecutableFixture(t *testing.T) (PublicationPreflightOptions, string) {
	return publishedFixtureWithExecutable(t, true)
}

func publishedFixtureWithExecutable(t *testing.T, executableNative bool) (PublicationPreflightOptions, string) {
	t.Helper()
	var signing ApprovedSigningOptions
	if executableNative {
		signing, _ = approvedSigningExecutableFixture(t)
	} else {
		signing, _ = approvedSigningFixture(t)
	}
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
	record.ReleaseNotesSHA256, record.Assets = publicationDigest(notes), assets
	record.CandidateRecordSHA256, record.LicenseEvidenceSHA256 = verified.CandidateRecordSHA256, verified.LicenseEvidenceSHA256
	record.SHA256SUMSSHA256, record.SignatureFileSHA256 = verified.SHA256SUMSSHA256, verified.SignatureFileSHA256
	record.TrustRecordSHA256, record.SigningAuthorizationSHA256 = verified.TrustRecordSHA256, verified.AuthorizationRecordSHA256
	body := canonicalPublicationAuthorization(t, record)
	recordFile := filepath.Join(t.TempDir(), "publication.json")
	if err = os.WriteFile(recordFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	return PublicationPreflightOptions{
		Verification: verification, PublicationAuthorizationFile: recordFile,
		ExpectedPublicationAuthorizationSHA256: publicationDigest(body),
		ExpectedRepository:                     record.Repository, ReleaseNotesFile: notesFile,
	}, signing.Dir
}

type fixtureReleaseReader struct {
	source           string
	corruptAfterCopy bool
}

func (f *fixtureReleaseReader) Verify(_ context.Context, plan githubverify.Plan) (githubverify.Observation, error) {
	if err := os.Mkdir(plan.DownloadDir, 0700); err != nil {
		return githubverify.Observation{}, err
	}
	assets := make([]githubverify.ObservedAsset, 0, len(plan.Assets))
	for i, asset := range plan.Assets {
		body, err := os.ReadFile(filepath.Join(f.source, asset.Name))
		if err != nil || int64(len(body)) != asset.Size {
			return githubverify.Observation{}, ErrPublicationAuthorization
		}
		if err = os.WriteFile(filepath.Join(plan.DownloadDir, asset.Name), body, 0644); err != nil {
			return githubverify.Observation{}, err
		}
		assets = append(assets, githubverify.ObservedAsset{
			ID: int64(i + 1), Name: asset.Name, Size: asset.Size, ContentType: publicationContentType(asset.Name), ServerSHA256: asset.SHA256,
			LocalSHA256: asset.SHA256, DownloadURL: "https://github.com/" + plan.Repository + "/releases/download/" + plan.Tag + "/" + asset.Name,
		})
	}
	if f.corruptAfterCopy {
		if err := os.WriteFile(filepath.Join(plan.DownloadDir, signatureName), []byte("replaced\n"), 0644); err != nil {
			return githubverify.Observation{}, err
		}
	}
	return githubverify.Observation{
		Repository: plan.Repository, ReleaseID: 7, ReleaseURL: "https://github.com/" + plan.Repository + "/releases/tag/" + plan.Tag,
		Tag: plan.Tag, Commit: plan.Commit, TagObjectSHA: strings.Repeat("c", 40), TagMessage: plan.TagMessage, Tagger: plan.Tagger,
		Title: plan.Title, BodySHA256: publicationDigest(plan.Body),
		Prerelease: plan.Prerelease, Immutable: true, PublishedAt: "2026-09-07T01:00:00Z",
		ObservedAt: "2026-09-07T01:01:00Z", Assets: assets, DownloadDir: plan.DownloadDir,
	}, nil
}
