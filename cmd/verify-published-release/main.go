// Command verify-published-release downloads and verifies one immutable GitHub
// release against independently supplied approval evidence. It is read-only on
// GitHub and writes only a new download directory and a new canonical receipt.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/githubverify"
	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

type readerFactory func(string, string) (releasepack.PublishedReleaseReader, error)
type receiptWriter func(string, releasepack.PostPublicationReceipt, ...string) (string, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, factory readerFactory, verify func(context.Context, releasepack.PublishedReleaseReader, releasepack.PublishedVerificationOptions) (releasepack.PostPublicationReceipt, error), write receiptWriter) int {
	flags := flag.NewFlagSet("verify-published-release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options releasepack.PublishedVerificationOptions
	var out, ghBinary, ghBinarySHA256 string
	p := &options.Preflight
	v := &p.Verification
	flags.StringVar(&v.Dir, "dir", "", "local approved signed release directory")
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
	flags.StringVar(&p.PublicationAuthorizationFile, "publication-authorization", "", "canonical external publication authorization")
	flags.StringVar(&p.ExpectedPublicationAuthorizationSHA256, "publication-authorization-sha256", "", "independently supplied publication-authorization digest")
	flags.StringVar(&p.ExpectedRepository, "repository", "", "independently approved GitHub owner/repository")
	flags.StringVar(&p.ReleaseNotesFile, "release-notes", "", "exact approved GitHub release-notes body")
	flags.StringVar(&options.VerifierID, "verifier-id", "", "independent verifier identity")
	flags.StringVar(&options.DownloadDir, "download-dir", "", "new directory for verified remote bytes")
	flags.StringVar(&out, "out", "", "new canonical post-publication receipt file")
	flags.StringVar(&ghBinary, "gh-binary", "", "absolute resolved gh executable")
	flags.StringVar(&ghBinarySHA256, "gh-binary-sha256", "", "independently supplied gh executable digest")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || ctx == nil || factory == nil || verify == nil || write == nil || out == "" || options.DownloadDir == "" ||
		ghBinary == "" || ghBinarySHA256 == "" ||
		!releasepack.ValidPostPublicationVerifierID(options.VerifierID) || missing(*p) {
		fmt.Fprintln(stderr, "all approved-release evidence, publication authorization, repository, release-notes, verifier-id, download-dir, out, gh-binary, and gh-binary-sha256 inputs are required and valid")
		return 2
	}
	reader, err := factory(ghBinary, ghBinarySHA256)
	if err != nil {
		fmt.Fprintln(stderr, "published release verification failed")
		return 1
	}
	receipt, err := verify(ctx, reader, options)
	if err != nil {
		fmt.Fprintln(stderr, "published release verification failed")
		return 1
	}
	digest, err := write(out, receipt, v.Source, v.Dir, options.DownloadDir)
	if err != nil {
		fmt.Fprintln(stderr, "publication receipt persistence failed")
		return 1
	}
	if _, err = fmt.Fprintln(stdout, digest); err != nil {
		fmt.Fprintln(stderr, "publication receipt result failed")
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
		o.PublicationAuthorizationFile == "" || o.ExpectedPublicationAuthorizationSHA256 == "" || o.ExpectedRepository == "" || o.ReleaseNotesFile == ""
}

func productionReader(ghBinary, ghBinarySHA256 string) (releasepack.PublishedReleaseReader, error) {
	attestor, err := githubverify.NewCommandReleaseAttestationVerifier(ghBinary, ghBinarySHA256)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return githubverify.New(githubverify.Config{
		APIBase: "https://api.github.com", Transport: transport, Now: time.Now,
		ReleaseAttestationVerifier: attestor,
	})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, productionReader, releasepack.VerifyPublishedRelease, releasepack.WritePostPublicationReceipt))
}
