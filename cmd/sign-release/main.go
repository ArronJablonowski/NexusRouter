// Command sign-release signs a local release using an explicitly supplied
// release-only Ed25519 seed. It never discovers credentials or uploads artifacts.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

func run(ctx context.Context, args []string, stderr io.Writer) int {
	return runWithSigner(ctx, args, stderr, releasepack.SignApproved)
}

func runWithSigner(ctx context.Context, args []string, stderr io.Writer, sign func(context.Context, releasepack.ApprovedSigningOptions) error) int {
	f := flag.NewFlagSet("sign-release", flag.ContinueOnError)
	f.SetOutput(stderr)
	dir := f.String("dir", "", "existing unsigned release directory")
	key := f.String("key", "", "release-only Ed25519 seed file (hex, mode 0400 or 0600)")
	candidate := f.String("candidate-record", "", "independently reviewed canonical candidate record")
	candidateSHA := f.String("candidate-record-sha256", "", "expected sha256 digest of exact candidate-record bytes")
	licenseEvidence := f.String("license-evidence", "", "canonical mechanical candidate license evidence")
	licenseEvidenceSHA := f.String("license-evidence-sha256", "", "expected sha256 digest of exact license-evidence bytes")
	source := f.String("source", "", "clean source repository at the candidate commit")
	sumsSHA := f.String("expected-sums-sha256", "", "approved sha256 digest of exact SHA256SUMS bytes")
	trust := f.String("trust-record", "", "independently trusted canonical public trust record")
	trustSHA := f.String("trust-record-sha256", "", "expected sha256 digest of exact trust-record bytes")
	keyID := f.String("key-id", "", "expected release-signing key ID")
	keyFingerprint := f.String("key-fingerprint", "", "expected sha256 public-key fingerprint")
	authorization := f.String("authorization-record", "", "external canonical production-signing authorization")
	authorizationSHA := f.String("authorization-record-sha256", "", "expected sha256 digest of exact authorization-record bytes")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *dir == "" || *key == "" || *candidate == "" || *candidateSHA == "" || *licenseEvidence == "" ||
		*licenseEvidenceSHA == "" || *source == "" ||
		*sumsSHA == "" || *trust == "" || *trustSHA == "" || *keyID == "" || *keyFingerprint == "" ||
		*authorization == "" || *authorizationSHA == "" || f.NArg() != 0 {
		fmt.Fprintln(stderr, "required: --dir --key --candidate-record --candidate-record-sha256 --license-evidence --license-evidence-sha256 --source --expected-sums-sha256 --trust-record --trust-record-sha256 --key-id --key-fingerprint --authorization-record --authorization-record-sha256; no positional arguments")
		return 2
	}
	options := releasepack.ApprovedSigningOptions{
		Dir: *dir, KeyFile: *key, CandidateRecordFile: *candidate, ExpectedCandidateSHA256: *candidateSHA,
		LicenseEvidenceFile: *licenseEvidence, ExpectedLicenseEvidenceSHA256: *licenseEvidenceSHA,
		Source: *source, ExpectedSumsSHA256: *sumsSHA, TrustRecordFile: *trust,
		ExpectedTrustRecordSHA256: *trustSHA, ExpectedKeyID: *keyID, ExpectedKeyFingerprint: *keyFingerprint,
		AuthorizationRecordFile: *authorization, ExpectedAuthorizationSHA256: *authorizationSHA,
	}
	if err := sign(ctx, options); err != nil {
		fmt.Fprintln(stderr, "release signing failed")
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr))
}
