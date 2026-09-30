// Command verify-approved-install verifies an approval-bound signed release,
// installs only its host-matching archive, executes it, and retains a receipt.
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
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

type approvedInstallVerifier func(context.Context, releasepack.ApprovedInstallVerificationOptions) (releasepack.ApprovedInstallVerificationReceipt, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, now func() time.Time, verify approvedInstallVerifier) int {
	if ctx == nil || stdout == nil || stderr == nil || now == nil || verify == nil {
		return 2
	}
	set := flag.NewFlagSet("verify-approved-install", flag.ContinueOnError)
	set.SetOutput(stderr)
	var options releasepack.ApprovedInstallVerificationOptions
	verification := &options.Verification
	var out string
	set.StringVar(&verification.Dir, "dir", "", "signed release directory")
	set.StringVar(&verification.Source, "source", "", "clean independently trusted source repository")
	set.StringVar(&verification.CandidateRecordFile, "candidate-record", "", "canonical external candidate record")
	set.StringVar(&verification.ExpectedCandidateSHA256, "candidate-record-sha256", "", "independently supplied candidate digest")
	set.StringVar(&verification.LicenseEvidenceFile, "license-evidence", "", "canonical mechanical candidate license evidence")
	set.StringVar(&verification.ExpectedLicenseEvidenceSHA256, "license-evidence-sha256", "", "independently supplied license-evidence digest")
	set.StringVar(&verification.ExpectedSumsSHA256, "expected-sums-sha256", "", "independently supplied SHA256SUMS digest")
	set.StringVar(&verification.TrustRecordFile, "trust-record", "", "independently retrieved canonical trust record")
	set.StringVar(&verification.ExpectedTrustRecordSHA256, "trust-record-sha256", "", "independently supplied trust-record digest")
	set.StringVar(&verification.ExpectedKeyID, "key-id", "", "expected release key ID")
	set.StringVar(&verification.ExpectedKeyFingerprint, "key-fingerprint", "", "expected release public-key fingerprint")
	set.StringVar(&verification.AuthorizationRecordFile, "authorization-record", "", "canonical external signing authorization")
	set.StringVar(&verification.ExpectedAuthorizationSHA256, "authorization-record-sha256", "", "independently supplied authorization digest")
	set.StringVar(&options.TargetOS, "target-os", "", "expected native operating system")
	set.StringVar(&options.TargetArch, "target-arch", "", "expected native architecture")
	set.StringVar(&options.InstallRoot, "install-root", "", "new private installation root")
	set.StringVar(&options.VerifierID, "verifier-id", "", "operator verification identity")
	set.StringVar(&options.HostID, "host-id", "", "operator-reviewed host identity")
	set.StringVar(&options.PublicKeyChannel, "public-key-channel", "", "independent HTTPS public-key channel")
	set.StringVar(&out, "out", "", "new canonical verification receipt")
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if set.NArg() != 0 || missing(options, out) {
		fmt.Fprintln(stderr, "all approval, target, install, verifier, host, public-key-channel, and output inputs are required")
		return 2
	}
	output, err := releasepack.PrepareApprovedInstallVerificationOutput(out, verification.Source, verification.Dir, options.InstallRoot)
	if err != nil {
		fmt.Fprintln(stderr, "approved install verification output reservation failed")
		return 1
	}
	defer output.Close()
	options.Now = func() time.Time { return now().UTC().Truncate(time.Second) }
	receipt, err := verify(ctx, options)
	if err != nil {
		fmt.Fprintln(stderr, "approved install verification failed")
		return 1
	}
	digest, err := output.Commit(receipt)
	if err != nil {
		fmt.Fprintln(stderr, "approved install verification receipt failed")
		return 1
	}
	if _, err = fmt.Fprintln(stdout, digest); err != nil {
		fmt.Fprintln(stderr, "approved install verification result failed")
		return 1
	}
	return 0
}

func missing(options releasepack.ApprovedInstallVerificationOptions, out string) bool {
	verification := options.Verification
	return verification.Dir == "" || verification.Source == "" || verification.CandidateRecordFile == "" ||
		verification.ExpectedCandidateSHA256 == "" || verification.LicenseEvidenceFile == "" ||
		verification.ExpectedLicenseEvidenceSHA256 == "" || verification.ExpectedSumsSHA256 == "" ||
		verification.TrustRecordFile == "" || verification.ExpectedTrustRecordSHA256 == "" ||
		verification.ExpectedKeyID == "" || verification.ExpectedKeyFingerprint == "" ||
		verification.AuthorizationRecordFile == "" || verification.ExpectedAuthorizationSHA256 == "" ||
		options.TargetOS == "" || options.TargetArch == "" || options.InstallRoot == "" ||
		options.VerifierID == "" || options.HostID == "" || options.PublicKeyChannel == "" || out == ""
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, time.Now, releasepack.VerifyApprovedInstall))
}
