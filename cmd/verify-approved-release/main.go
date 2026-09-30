// Command verify-approved-release independently binds a production signature
// and all release bytes to the exact reviewed public approval inputs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

type approvedVerifier func(context.Context, releasepack.ApprovedVerificationOptions) (releasepack.ApprovedVerificationResult, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, verify approvedVerifier) int {
	flags := flag.NewFlagSet("verify-approved-release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options releasepack.ApprovedVerificationOptions
	flags.StringVar(&options.Dir, "dir", "", "signed release directory")
	flags.StringVar(&options.Source, "source", ".", "clean independently trusted source repository")
	flags.StringVar(&options.CandidateRecordFile, "candidate-record", "", "canonical external candidate record")
	flags.StringVar(&options.ExpectedCandidateSHA256, "candidate-record-sha256", "", "independently supplied candidate digest")
	flags.StringVar(&options.LicenseEvidenceFile, "license-evidence", "", "canonical mechanical candidate license evidence")
	flags.StringVar(&options.ExpectedLicenseEvidenceSHA256, "license-evidence-sha256", "", "independently supplied license-evidence digest")
	flags.StringVar(&options.ExpectedSumsSHA256, "expected-sums-sha256", "", "independently supplied SHA256SUMS digest")
	flags.StringVar(&options.TrustRecordFile, "trust-record", "", "independently retrieved canonical trust record")
	flags.StringVar(&options.ExpectedTrustRecordSHA256, "trust-record-sha256", "", "independently supplied trust-record digest")
	flags.StringVar(&options.ExpectedKeyID, "key-id", "", "expected release key ID")
	flags.StringVar(&options.ExpectedKeyFingerprint, "key-fingerprint", "", "expected release public-key fingerprint")
	flags.StringVar(&options.AuthorizationRecordFile, "authorization-record", "", "canonical external signing authorization")
	flags.StringVar(&options.ExpectedAuthorizationSHA256, "authorization-record-sha256", "", "independently supplied authorization digest")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || verify == nil || missing(options) {
		fmt.Fprintln(stderr, "all release, source, candidate, license-evidence, checksum, trust, key, and authorization inputs are required")
		return 2
	}
	result, err := verify(ctx, options)
	if err != nil {
		fmt.Fprintln(stderr, "approved release verification failed")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(result); err != nil {
		fmt.Fprintln(stderr, "approved release verification result failed")
		return 1
	}
	return 0
}

func missing(options releasepack.ApprovedVerificationOptions) bool {
	return options.Dir == "" || options.Source == "" || options.CandidateRecordFile == "" ||
		options.ExpectedCandidateSHA256 == "" || options.LicenseEvidenceFile == "" ||
		options.ExpectedLicenseEvidenceSHA256 == "" || options.ExpectedSumsSHA256 == "" ||
		options.TrustRecordFile == "" || options.ExpectedTrustRecordSHA256 == "" ||
		options.ExpectedKeyID == "" || options.ExpectedKeyFingerprint == "" ||
		options.AuthorizationRecordFile == "" || options.ExpectedAuthorizationSHA256 == ""
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, releasepack.VerifyApproved))
}
