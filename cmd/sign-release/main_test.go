package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func TestArguments(t *testing.T) {
	complete := []string{
		"--dir", t.TempDir(), "--key", "missing-key", "--candidate-record", "missing-candidate",
		"--candidate-record-sha256", "sha256:" + strings.Repeat("0", 64), "--source", t.TempDir(),
		"--expected-sums-sha256", "sha256:" + strings.Repeat("0", 64), "--trust-record", "missing-trust",
		"--trust-record-sha256", "sha256:" + strings.Repeat("0", 64), "--key-id", "release-test-01",
		"--key-fingerprint", "sha256:" + strings.Repeat("0", 64),
		"--authorization-record", "missing-authorization", "--authorization-record-sha256", "sha256:" + strings.Repeat("0", 64),
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"--help"}, 0},
		{[]string{"--unknown"}, 2},
		{[]string{"--dir", "x"}, 2},
		{[]string{"--key", "x"}, 2},
		{[]string{"--dir", "x", "--key", "y", "extra"}, 2},
		{complete[:len(complete)-4], 2},
		{complete[:len(complete)-2], 2},
		{complete, 1},
	} {
		var out bytes.Buffer
		if got := run(context.Background(), tc.args, &out); got != tc.code {
			t.Fatalf("%q: code %d, want %d", tc.args, got, tc.code)
		}
	}
}

func TestApprovedArgumentsForwarded(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	args := []string{
		"--dir", "/release", "--key", "/secret/seed", "--candidate-record", "/evidence/candidate.json",
		"--candidate-record-sha256", digest, "--source", "/source", "--expected-sums-sha256", digest,
		"--trust-record", "/trust/record.json", "--trust-record-sha256", digest,
		"--key-id", "release-2026-01", "--key-fingerprint", digest,
		"--authorization-record", "/evidence/authorization.json", "--authorization-record-sha256", digest,
	}
	want := releasepack.ApprovedSigningOptions{
		Dir: "/release", KeyFile: "/secret/seed", CandidateRecordFile: "/evidence/candidate.json",
		ExpectedCandidateSHA256: digest, Source: "/source", ExpectedSumsSHA256: digest,
		TrustRecordFile: "/trust/record.json", ExpectedTrustRecordSHA256: digest,
		ExpectedKeyID: "release-2026-01", ExpectedKeyFingerprint: digest,
		AuthorizationRecordFile: "/evidence/authorization.json", ExpectedAuthorizationSHA256: digest,
	}
	called := false
	signer := func(ctx context.Context, got releasepack.ApprovedSigningOptions) error {
		called = true
		if ctx == nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("forwarded options: %#v", got)
		}
		return nil
	}
	var output bytes.Buffer
	if code := runWithSigner(context.Background(), args, &output, signer); code != 0 || !called || output.Len() != 0 {
		t.Fatalf("code=%d called=%t output=%q", code, called, output.String())
	}
}
