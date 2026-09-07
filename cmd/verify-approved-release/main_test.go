package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func TestApprovedVerificationArgumentsAndResult(t *testing.T) {
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	args := []string{
		"--dir", "/release", "--source", "/source", "--candidate-record", "/candidate",
		"--candidate-record-sha256", digest, "--expected-sums-sha256", digest,
		"--license-evidence", "/license-evidence", "--license-evidence-sha256", digest,
		"--trust-record", "/trust", "--trust-record-sha256", digest,
		"--key-id", "release-1", "--key-fingerprint", digest,
		"--authorization-record", "/authorization", "--authorization-record-sha256", digest,
	}
	var captured releasepack.ApprovedVerificationOptions
	var stdout, stderr bytes.Buffer
	result := releasepack.ApprovedVerificationResult{
		CandidateRecordSHA256: digest, SHA256SUMSSHA256: digest, TrustRecordSHA256: digest,
		LicenseEvidenceSHA256:     digest,
		AuthorizationRecordSHA256: digest, KeyID: "release-1", KeyFingerprint: digest, SignatureFileSHA256: digest,
	}
	code := run(context.Background(), args, &stdout, &stderr, func(_ context.Context, options releasepack.ApprovedVerificationOptions) (releasepack.ApprovedVerificationResult, error) {
		captured = options
		return result, nil
	})
	if code != 0 || stderr.Len() != 0 || captured.Dir != "/release" || captured.Source != "/source" ||
		captured.CandidateRecordFile != "/candidate" || captured.ExpectedCandidateSHA256 != digest ||
		captured.LicenseEvidenceFile != "/license-evidence" || captured.ExpectedLicenseEvidenceSHA256 != digest ||
		captured.ExpectedSumsSHA256 != digest || captured.TrustRecordFile != "/trust" ||
		captured.ExpectedTrustRecordSHA256 != digest || captured.ExpectedKeyID != "release-1" ||
		captured.ExpectedKeyFingerprint != digest || captured.AuthorizationRecordFile != "/authorization" ||
		captured.ExpectedAuthorizationSHA256 != digest {
		t.Fatalf("arguments not forwarded: code=%d options=%+v stderr=%q", code, captured, stderr.String())
	}
	want := "{\"candidate_record_sha256\":\"" + digest + "\",\"license_evidence_sha256\":\"" + digest + "\",\"sha256sums_sha256\":\"" + digest + "\",\"trust_record_sha256\":\"" + digest + "\",\"authorization_record_sha256\":\"" + digest + "\",\"key_id\":\"release-1\",\"key_fingerprint\":\"" + digest + "\",\"signature_file_sha256\":\"" + digest + "\"}\n"
	if stdout.String() != want {
		t.Fatalf("unexpected result: %q", stdout.String())
	}
}

func TestApprovedVerificationCLIRejectsMissingAndFailure(t *testing.T) {
	if code := run(context.Background(), nil, &bytes.Buffer{}, &bytes.Buffer{}, nil); code != 2 {
		t.Fatal("missing arguments accepted", code)
	}
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	args := []string{
		"--dir", "d", "--source", "s", "--candidate-record", "c", "--candidate-record-sha256", digest,
		"--license-evidence", "l", "--license-evidence-sha256", digest,
		"--expected-sums-sha256", digest, "--trust-record", "t", "--trust-record-sha256", digest,
		"--key-id", "k", "--key-fingerprint", digest, "--authorization-record", "a",
		"--authorization-record-sha256", digest,
	}
	calls := 0
	code := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, func(context.Context, releasepack.ApprovedVerificationOptions) (releasepack.ApprovedVerificationResult, error) {
		calls++
		return releasepack.ApprovedVerificationResult{}, errors.New("injected")
	})
	if code != 1 || calls != 1 {
		t.Fatalf("failure code=%d calls=%d", code, calls)
	}
}

func TestApprovedVerificationRequiresLicenseEvidenceArguments(t *testing.T) {
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	complete := []string{
		"--dir", "d", "--source", "s", "--candidate-record", "c", "--candidate-record-sha256", digest,
		"--license-evidence", "l", "--license-evidence-sha256", digest, "--expected-sums-sha256", digest,
		"--trust-record", "t", "--trust-record-sha256", digest, "--key-id", "i",
		"--key-fingerprint", digest, "--authorization-record", "a", "--authorization-record-sha256", digest,
	}
	for _, omitted := range []string{"--license-evidence", "--license-evidence-sha256"} {
		args := removeFlagPair(complete, omitted)
		called := false
		code := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, func(context.Context, releasepack.ApprovedVerificationOptions) (releasepack.ApprovedVerificationResult, error) {
			called = true
			return releasepack.ApprovedVerificationResult{}, nil
		})
		if code != 2 || called {
			t.Fatalf("missing %s: code=%d called=%t", omitted, code, called)
		}
	}
}

func removeFlagPair(args []string, omitted string) []string {
	result := make([]string, 0, len(args)-2)
	for i := 0; i < len(args); i += 2 {
		if args[i] != omitted {
			result = append(result, args[i], args[i+1])
		}
	}
	return result
}
