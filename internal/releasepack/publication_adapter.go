package releasepack

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/internal/githubpublish"
)

type AuthorizedPublicationOptions struct {
	Preflight   PublicationPreflightOptions
	JournalPath string
}

// PublishAuthorizedReleaseWithCredential is the production credential seam.
// Credential acquisition occurs only after both authorization preflights close,
// and githubpublish confines the lease to exactly one PublishRelease call.
func PublishAuthorizedReleaseWithCredential(ctx context.Context, config githubpublish.CredentialPublicationConfig, options AuthorizedPublicationOptions) (githubpublish.PublicationEvidence, error) {
	return PublishAuthorizedRelease(ctx, credentialedReleasePublisher{config: config}, options)
}

type credentialedReleasePublisher struct {
	config githubpublish.CredentialPublicationConfig
}

func (p credentialedReleasePublisher) PublishRelease(ctx context.Context, plan githubpublish.PublicationPlan) (githubpublish.PublicationEvidence, error) {
	return githubpublish.PublishCredentialedRelease(ctx, p.config, plan)
}

// AuthorizedReleasePublisher is the sole mutation seam. Implementations receive
// only cloned bytes after both preflights and all identity checks have closed.
type AuthorizedReleasePublisher interface {
	PublishRelease(context.Context, githubpublish.PublicationPlan) (githubpublish.PublicationEvidence, error)
}

type publicationPreflightFunc func(context.Context, PublicationPreflightOptions) (PublicationPreflightResult, error)

// PublishAuthorizedRelease derives a full publication plan exclusively from the
// approval-bound local evidence. No publisher method is called on any failure.
func PublishAuthorizedRelease(ctx context.Context, publisher AuthorizedReleasePublisher, options AuthorizedPublicationOptions) (githubpublish.PublicationEvidence, error) {
	return publishAuthorizedReleaseWithVerifier(ctx, publisher, options, VerifyPublicationPreflight)
}

func publishAuthorizedReleaseWithVerifier(ctx context.Context, publisher AuthorizedReleasePublisher, options AuthorizedPublicationOptions, verify publicationPreflightFunc) (githubpublish.PublicationEvidence, error) {
	var empty githubpublish.PublicationEvidence
	if ctx == nil || publisher == nil || verify == nil || ctx.Err() != nil {
		return empty, ErrPublicationAuthorization
	}
	pins, err := pinPublicationInputs(options.Preflight, options.JournalPath)
	if err != nil {
		return empty, ErrPublicationAuthorization
	}
	preflight, err := verify(ctx, options.Preflight)
	if err != nil || len(preflight.Assets) != 7 {
		return empty, ErrPublicationAuthorization
	}
	notes, err := readPublicationInput(options.Preflight.ReleaseNotesFile, maxReleaseNotes)
	if err != nil || publicationDigest(notes) != preflight.ReleaseNotesSHA256 {
		return empty, ErrPublicationAuthorization
	}
	owner, repository, ok := strings.Cut(preflight.Repository, "/")
	if !ok || owner == "" || repository == "" {
		return empty, ErrPublicationAuthorization
	}
	assets := make([]githubpublish.Asset, len(preflight.Assets))
	for i, expected := range preflight.Assets {
		body, readErr := readPublicationAsset(options.Preflight.Verification.Dir, expected)
		if readErr != nil {
			return empty, ErrPublicationAuthorization
		}
		assets[i] = githubpublish.Asset{Name: expected.Name, ContentType: publicationContentType(expected.Name), SHA256: expected.SHA256, Body: body}
	}
	closing, err := verify(ctx, options.Preflight)
	if err != nil || !samePublicationPreflight(preflight, closing) || ctx.Err() != nil || revalidatePublicationPins(pins) != nil || publicationJournalAbsent(options.JournalPath) != nil {
		return empty, ErrPublicationAuthorization
	}
	plan := githubpublish.PublicationPlan{DraftPlan: githubpublish.DraftPlan{
		AuthorizationSHA256: preflight.PublicationAuthorizationSHA256,
		ReleaseNotesSHA256:  preflight.ReleaseNotesSHA256,
		Owner:               owner, Repository: repository, Tag: preflight.Tag, Commit: preflight.SourceCommit,
		Name: preflight.ReleaseTitle, Body: string(notes), Draft: true,
		Prerelease: preflight.Prerelease, Assets: assets,
	}, TagMessage: preflight.TagMessage,
		Tagger:     githubpublish.Tagger{Name: preflight.Tagger.Name, Email: preflight.Tagger.Email, Date: preflight.Tagger.Date},
		MakeLatest: preflight.MakeLatest, JournalPath: options.JournalPath}
	return publisher.PublishRelease(ctx, plan)
}

type publicationPin struct {
	path string
	info os.FileInfo
}

func pinPublicationInputs(options PublicationPreflightOptions, journalPath string) ([]publicationPin, error) {
	if !filepath.IsAbs(journalPath) || filepath.Clean(journalPath) != journalPath || publicationJournalAbsent(journalPath) != nil ||
		publicationJournalOutsideRoots(journalPath, options.Verification.Source, options.Verification.Dir) != nil {
		return nil, ErrPublicationAuthorization
	}
	paths := []string{
		options.Verification.Source, options.Verification.Dir, filepath.Dir(journalPath),
		options.PublicationAuthorizationFile, options.ReleaseNotesFile,
		options.Verification.CandidateRecordFile, options.Verification.LicenseEvidenceFile,
		options.Verification.TrustRecordFile, options.Verification.AuthorizationRecordFile,
	}
	entries, err := os.ReadDir(options.Verification.Dir)
	if err != nil || len(entries) != 7 {
		return nil, ErrPublicationAuthorization
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !signingBasename(entry.Name()) {
			return nil, ErrPublicationAuthorization
		}
		paths = append(paths, filepath.Join(options.Verification.Dir, entry.Name()))
	}
	pins := make([]publicationPin, len(paths))
	for i, path := range paths {
		info, statErr := os.Lstat(path)
		wantDir := i < 3
		if statErr != nil || wantDir != info.IsDir() || !wantDir && !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrPublicationAuthorization
		}
		pins[i] = publicationPin{path: path, info: info}
	}
	return pins, nil
}

func publicationJournalOutsideRoots(path string, roots ...string) error {
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return ErrPublicationAuthorization
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return ErrPublicationAuthorization
	}
	realParent, err = filepath.Abs(realParent)
	if err != nil {
		return ErrPublicationAuthorization
	}
	candidate := filepath.Join(realParent, filepath.Base(path))
	for _, root := range roots {
		realRoot, rootErr := filepath.EvalSymlinks(root)
		if rootErr != nil {
			return ErrPublicationAuthorization
		}
		realRoot, rootErr = filepath.Abs(realRoot)
		if rootErr != nil {
			return ErrPublicationAuthorization
		}
		rel, relErr := filepath.Rel(realRoot, candidate)
		if relErr != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return ErrPublicationAuthorization
		}
	}
	return nil
}

func publicationJournalAbsent(path string) error {
	_, err := os.Lstat(path)
	if !os.IsNotExist(err) {
		return ErrPublicationAuthorization
	}
	return nil
}

func revalidatePublicationPins(pins []publicationPin) error {
	for _, pin := range pins {
		info, err := os.Lstat(pin.path)
		if err != nil || !os.SameFile(pin.info, info) || pin.info.Mode() != info.Mode() || pin.info.Size() != info.Size() || !pin.info.ModTime().Equal(info.ModTime()) {
			return ErrPublicationAuthorization
		}
	}
	return nil
}

func readPublicationAsset(dir string, expected PublicationAsset) ([]byte, error) {
	root, err := releaseRoot(dir)
	if err != nil {
		return nil, ErrPublicationAuthorization
	}
	defer root.Close()
	body, err := readReleaseFile(root, expected.Name, 256<<20)
	if err != nil || int64(len(body)) != expected.Size || publicationDigest(body) != expected.SHA256 {
		return nil, ErrPublicationAuthorization
	}
	return body, nil
}

func samePublicationPreflight(a, b PublicationPreflightResult) bool {
	return a.PublicationAuthorizationSHA256 == b.PublicationAuthorizationSHA256 && a.Repository == b.Repository &&
		a.ReleaseVersion == b.ReleaseVersion && a.SourceCommit == b.SourceCommit && a.Tag == b.Tag &&
		a.ReleaseTitle == b.ReleaseTitle && a.TagMessage == b.TagMessage && a.Tagger == b.Tagger &&
		a.Prerelease == b.Prerelease && a.MakeLatest == b.MakeLatest &&
		a.ReleaseNotesSHA256 == b.ReleaseNotesSHA256 && a.PublicationApproverID == b.PublicationApproverID && equalPublicationAssets(a.Assets, b.Assets)
}
