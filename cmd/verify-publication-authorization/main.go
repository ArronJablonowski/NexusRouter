// Command verify-publication-authorization performs a fully offline preflight.
// It cannot generate approval, access GitHub, tag, upload, sign, or publish.
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

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

type preflightVerifier func(context.Context, releasepack.PublicationPreflightOptions) (releasepack.PublicationPreflightResult, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, verify preflightVerifier) int {
	flags := flag.NewFlagSet("verify-publication-authorization", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options releasepack.PublicationPreflightOptions
	v := &options.Verification
	flags.StringVar(&v.Dir, "dir", "", "signed release directory")
	flags.StringVar(&v.Source, "source", "", "clean independently trusted source repository")
	flags.StringVar(&v.CandidateRecordFile, "candidate-record", "", "canonical external candidate record")
	flags.StringVar(&v.ExpectedCandidateSHA256, "candidate-record-sha256", "", "independently supplied candidate digest")
	flags.StringVar(&v.LicenseEvidenceFile, "license-evidence", "", "canonical candidate license evidence")
	flags.StringVar(&v.ExpectedLicenseEvidenceSHA256, "license-evidence-sha256", "", "independently supplied license-evidence digest")
	flags.StringVar(&v.ExpectedSumsSHA256, "expected-sums-sha256", "", "independently supplied SHA256SUMS digest")
	flags.StringVar(&v.TrustRecordFile, "trust-record", "", "independently retrieved canonical trust record")
	flags.StringVar(&v.ExpectedTrustRecordSHA256, "trust-record-sha256", "", "independently supplied trust-record digest")
	flags.StringVar(&v.ExpectedKeyID, "key-id", "", "expected release key ID")
	flags.StringVar(&v.ExpectedKeyFingerprint, "key-fingerprint", "", "expected public-key fingerprint")
	flags.StringVar(&v.AuthorizationRecordFile, "signing-authorization", "", "canonical external signing authorization")
	flags.StringVar(&v.ExpectedAuthorizationSHA256, "signing-authorization-sha256", "", "independently supplied signing-authorization digest")
	flags.StringVar(&options.PublicationAuthorizationFile, "publication-authorization", "", "canonical external publication authorization")
	flags.StringVar(&options.ExpectedPublicationAuthorizationSHA256, "publication-authorization-sha256", "", "independently supplied publication-authorization digest")
	flags.StringVar(&options.ExpectedRepository, "repository", "", "independently approved GitHub owner/repository")
	flags.StringVar(&options.ReleaseNotesFile, "release-notes", "", "exact approved GitHub release-notes body")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || verify == nil || missing(options) {
		fmt.Fprintln(stderr, "all signed-release evidence, publication authorization, repository, and release-notes inputs are required")
		return 2
	}
	result, err := verify(ctx, options)
	if err != nil {
		fmt.Fprintln(stderr, "publication authorization preflight failed")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(result) != nil {
		fmt.Fprintln(stderr, "publication authorization result failed")
		return 1
	}
	return 0
}

func missing(o releasepack.PublicationPreflightOptions) bool {
	v := o.Verification
	return v.Dir == "" || v.Source == "" || v.CandidateRecordFile == "" || v.ExpectedCandidateSHA256 == "" ||
		v.LicenseEvidenceFile == "" || v.ExpectedLicenseEvidenceSHA256 == "" || v.ExpectedSumsSHA256 == "" ||
		v.TrustRecordFile == "" || v.ExpectedTrustRecordSHA256 == "" || v.ExpectedKeyID == "" ||
		v.ExpectedKeyFingerprint == "" || v.AuthorizationRecordFile == "" || v.ExpectedAuthorizationSHA256 == "" ||
		o.PublicationAuthorizationFile == "" || o.ExpectedPublicationAuthorizationSHA256 == "" ||
		o.ExpectedRepository == "" || o.ReleaseNotesFile == ""
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, releasepack.VerifyPublicationPreflight))
}
