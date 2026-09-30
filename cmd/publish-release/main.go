// Command publish-release performs one approval-bound, create-only GitHub
// publication. It cannot generate approvals, overwrite a tag or release, or
// accept a credential in process arguments.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/githubpublish"
	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

const (
	githubAPIOrigin    = "https://api.github.com"
	githubUploadOrigin = "https://uploads.github.com"
)

type releasePublisher func(context.Context, githubpublish.CredentialPublicationConfig, releasepack.AuthorizedPublicationOptions) (githubpublish.PublicationEvidence, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, transport githubpublish.RoundTripper, publish releasePublisher) int {
	flags := flag.NewFlagSet("publish-release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options releasepack.AuthorizedPublicationOptions
	p := &options.Preflight
	v := &p.Verification
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
	flags.StringVar(&p.PublicationAuthorizationFile, "publication-authorization", "", "canonical external publication authorization")
	flags.StringVar(&p.ExpectedPublicationAuthorizationSHA256, "publication-authorization-sha256", "", "independently supplied publication-authorization digest")
	flags.StringVar(&p.ExpectedRepository, "repository", "", "independently approved GitHub owner/repository")
	flags.StringVar(&p.ReleaseNotesFile, "release-notes", "", "exact approved GitHub release-notes body")
	flags.StringVar(&options.JournalPath, "journal", "", "new durable publication journal outside source and release directories")
	credentialFD := flags.Int("credential-fd", 0, "already-open descriptor containing the GitHub token (default: stdin)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || transport == nil || publish == nil || missing(options) {
		fmt.Fprintln(stderr, "all signed-release evidence, publication authorization, repository, release-notes, journal, and credential-fd inputs are required")
		return 2
	}
	secrets, err := githubpublish.NewFDCredentialSource(*credentialFD)
	if err != nil {
		fmt.Fprintln(stderr, "release publication credential source rejected")
		return 1
	}
	evidence, publishErr := publish(ctx, githubpublish.CredentialPublicationConfig{
		APIBase: githubAPIOrigin, UploadBase: githubUploadOrigin, Transport: transport, Secrets: secrets,
	}, options)
	if evidence.SchemaVersion != 0 {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if err = encoder.Encode(evidence); err != nil {
			fmt.Fprintln(stderr, "release publication evidence failed")
			return 1
		}
	}
	if publishErr != nil {
		fmt.Fprintln(stderr, "release publication failed; inspect the durable journal before any retry")
		return 1
	}
	return 0
}

func missing(o releasepack.AuthorizedPublicationOptions) bool {
	p, v := o.Preflight, o.Preflight.Verification
	return v.Dir == "" || v.Source == "" || v.CandidateRecordFile == "" || v.ExpectedCandidateSHA256 == "" ||
		v.LicenseEvidenceFile == "" || v.ExpectedLicenseEvidenceSHA256 == "" || v.ExpectedSumsSHA256 == "" ||
		v.TrustRecordFile == "" || v.ExpectedTrustRecordSHA256 == "" || v.ExpectedKeyID == "" ||
		v.ExpectedKeyFingerprint == "" || v.AuthorizationRecordFile == "" || v.ExpectedAuthorizationSHA256 == "" ||
		p.PublicationAuthorizationFile == "" || p.ExpectedPublicationAuthorizationSHA256 == "" ||
		p.ExpectedRepository == "" || p.ReleaseNotesFile == "" || o.JournalPath == ""
}

func releaseHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	transport := releaseHTTPTransport()
	defer transport.CloseIdleConnections()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, transport, releasepack.PublishAuthorizedReleaseWithCredential))
}
