package releasepack

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// ApprovedSigningOptions binds the production signing operation to externally
// reviewed candidate, artifact-set and trust identities. Expected digests use
// "sha256:" followed by 64 lowercase hexadecimal characters.
type ApprovedSigningOptions struct {
	Dir                         string
	KeyFile                     string
	CandidateRecordFile         string
	ExpectedCandidateSHA256     string
	Source                      string
	ExpectedSumsSHA256          string
	TrustRecordFile             string
	ExpectedTrustRecordSHA256   string
	ExpectedKeyID               string
	ExpectedKeyFingerprint      string
	AuthorizationRecordFile     string
	ExpectedAuthorizationSHA256 string
}

// SignApproved validates every public input before opening the private seed,
// proves the seed belongs to the approved trust identity, exclusively writes a
// durable signature and immediately verifies the complete signed directory.
// It never approves a candidate, fetches records, publishes, tags or uploads.
func SignApproved(ctx context.Context, options ApprovedSigningOptions) error {
	return signApproved(ctx, options, func(path string) ([]byte, error) {
		return signingKeyFile(path, true)
	})
}

// privateKeyReader returns an owned Ed25519 seed buffer. It is injected per
// call only so tests can prove preflight ordering without mutable global state.
type privateKeyReader func(path string) ([]byte, error)

func signApproved(ctx context.Context, options ApprovedSigningOptions, readPrivateKey privateKeyReader) error {
	if ctx == nil || options.Dir == "" || options.KeyFile == "" || options.Source == "" ||
		options.CandidateRecordFile == "" || options.TrustRecordFile == "" || options.AuthorizationRecordFile == "" ||
		!trustFingerprint(options.ExpectedCandidateSHA256) || !trustFingerprint(options.ExpectedSumsSHA256) || readPrivateKey == nil {
		return ErrSignature
	}
	if err := ctx.Err(); err != nil {
		return ErrSignature
	}
	candidateBody, candidate, err := readCandidateRecord(options.CandidateRecordFile)
	if err != nil || prefixedDigest(candidateBody) != options.ExpectedCandidateSHA256 ||
		verifyCandidateRecord(ctx, candidate, options.Source) != nil {
		return ErrSignature
	}
	source, err := filepath.Abs(options.Source)
	if err != nil {
		return ErrSignature
	}
	authorization, err := ReadSigningAuthorization(options.AuthorizationRecordFile, SigningAuthorizationExpectations{
		RecordSHA256: options.ExpectedAuthorizationSHA256, CandidateRecordSHA256: options.ExpectedCandidateSHA256,
		SHA256SUMSSHA256: options.ExpectedSumsSHA256, TrustRecordSHA256: options.ExpectedTrustRecordSHA256,
		KeyID: options.ExpectedKeyID, KeyFingerprint: options.ExpectedKeyFingerprint,
	})
	if err != nil {
		return ErrSignature
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return ErrSignature
	}
	trustRecord, public, err := readExpectedTrustRecord(options.TrustRecordFile, options.ExpectedKeyID,
		options.ExpectedKeyFingerprint, options.ExpectedTrustRecordSHA256)
	if err != nil || authorization.ReleasePolicyURL != trustRecord.ReleasePolicyURL {
		return ErrSignature
	}
	root, sums, err := checkedRelease(options.Dir, false)
	if err != nil {
		return ErrSignature
	}
	if prefixedDigest(sums) != options.ExpectedSumsSHA256 || approvedArtifactIdentity(root, candidate) != nil {
		root.Close()
		return ErrSignature
	}
	if err = ctx.Err(); err != nil || verifyCandidateCheckout(ctx, source, candidate.SourceCommit, environment()) != nil {
		root.Close()
		return ErrSignature
	}

	// No private material is opened until all candidate, source, trust and
	// artifact identities above have been validated.
	seed, err := readPrivateKey(options.KeyFile)
	if err != nil || len(seed) != ed25519.SeedSize {
		clear(seed)
		root.Close()
		return ErrSignature
	}
	defer clear(seed)
	private := ed25519.NewKeyFromSeed(seed)
	defer clear(private)
	derived, ok := private.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(derived, public) || ctx.Err() != nil {
		root.Close()
		return ErrSignature
	}
	if err = writeSignature(root, sums, private); err != nil {
		root.Close()
		return ErrSignature
	}
	closeErr := root.Close()
	verifyErr := verifyWithKey(options.Dir, public)
	if closeErr != nil || verifyErr != nil {
		return ErrSignature
	}
	// A late source change cannot alter the already packaged bytes, but it makes
	// the production ceremony evidence inconsistent and therefore fails closed.
	if verifyCandidateCheckout(ctx, source, candidate.SourceCommit, environment()) != nil {
		return ErrSignature
	}
	return nil
}

func approvedArtifactIdentity(root *os.Root, candidate CandidateRecord) error {
	body, err := readReleaseFile(root, "manifest.json", 64<<10)
	if err != nil {
		return ErrSignature
	}
	var manifest Manifest
	if json.Unmarshal(body, &manifest) != nil || manifest.SchemaVersion != candidate.ReleaseManifestSchema ||
		manifest.Version != candidate.ReleaseVersion || manifest.Commit != candidate.SourceCommit ||
		len(manifest.Artifacts) != len(candidate.Targets) {
		return ErrSignature
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return ErrSignature
	}
	for i, target := range candidate.Targets {
		artifact := manifest.Artifacts[i]
		if artifact.OS != target.OS || artifact.Arch != target.Arch || len(artifact.Entries) != len(candidate.ArchiveEntries) {
			return ErrSignature
		}
	}
	indexes := [...]int{0, 1, 2, 4}
	if len(candidate.SourceCollateral) != len(indexes) {
		return ErrSignature
	}
	for i, index := range indexes {
		if manifest.Artifacts[0].Entries[index].SHA256 != candidate.SourceCollateral[i].SHA256 {
			return ErrSignature
		}
	}
	return nil
}

func prefixedDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}
