package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func TestPublicationPreflightCLIForwardsAuthority(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	args := []string{
		"--dir", "/release", "--source", "/source", "--candidate-record", "/candidate", "--candidate-record-sha256", digest,
		"--license-evidence", "/license", "--license-evidence-sha256", digest, "--expected-sums-sha256", digest,
		"--trust-record", "/trust", "--trust-record-sha256", digest, "--key-id", "release-1", "--key-fingerprint", digest,
		"--signing-authorization", "/signing", "--signing-authorization-sha256", digest,
		"--publication-authorization", "/publication", "--publication-authorization-sha256", digest,
		"--repository", "ArronJablonowski/DarwinRouter", "--release-notes", "/notes",
	}
	var captured releasepack.PublicationPreflightOptions
	var out, diagnostic bytes.Buffer
	result := releasepack.PublicationPreflightResult{PublicationAuthorizationSHA256: digest, Repository: "ArronJablonowski/DarwinRouter", ReleaseVersion: "1.0.0", SourceCommit: strings.Repeat("a", 40), Tag: "v1.0.0", ReleaseNotesSHA256: digest}
	code := run(context.Background(), args, &out, &diagnostic, func(_ context.Context, got releasepack.PublicationPreflightOptions) (releasepack.PublicationPreflightResult, error) {
		captured = got
		return result, nil
	})
	if code != 0 || diagnostic.Len() != 0 || captured.ExpectedRepository != "ArronJablonowski/DarwinRouter" ||
		captured.PublicationAuthorizationFile != "/publication" || captured.Verification.AuthorizationRecordFile != "/signing" ||
		captured.Verification.ExpectedLicenseEvidenceSHA256 != digest || !strings.Contains(out.String(), `"tag":"v1.0.0"`) {
		t.Fatalf("CLI did not preserve authority inputs: code=%d options=%+v out=%q err=%q", code, captured, out.String(), diagnostic.String())
	}
}

func TestPublicationPreflightCLIRejectsMissingAndFailure(t *testing.T) {
	if code := run(context.Background(), nil, &bytes.Buffer{}, &bytes.Buffer{}, nil); code != 2 {
		t.Fatal("missing inputs accepted", code)
	}
	digest := "sha256:" + strings.Repeat("1", 64)
	args := []string{"--dir", "d", "--source", "s", "--candidate-record", "c", "--candidate-record-sha256", digest,
		"--license-evidence", "l", "--license-evidence-sha256", digest, "--expected-sums-sha256", digest,
		"--trust-record", "t", "--trust-record-sha256", digest, "--key-id", "k", "--key-fingerprint", digest,
		"--signing-authorization", "a", "--signing-authorization-sha256", digest,
		"--publication-authorization", "p", "--publication-authorization-sha256", digest,
		"--repository", "o/r", "--release-notes", "n"}
	called := false
	code := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, func(context.Context, releasepack.PublicationPreflightOptions) (releasepack.PublicationPreflightResult, error) {
		called = true
		return releasepack.PublicationPreflightResult{}, errors.New("injected")
	})
	if code != 1 || !called {
		t.Fatal("verification failure not propagated", code, called)
	}
}
