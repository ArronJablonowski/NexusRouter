package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

func TestRunVerifiesExactApprovedInstallReceipt(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--record", "/evidence/approved-install.json", "--record-sha256", digest}, &stdout, &stderr,
		func(path, gotDigest string) (releasepack.ApprovedInstallVerificationReceipt, error) {
			if path != "/evidence/approved-install.json" || gotDigest != digest {
				t.Fatal("verification inputs changed", path, gotDigest)
			}
			return releasepack.ApprovedInstallVerificationReceipt{SchemaVersion: 1, Scope: "fixture"}, nil
		})
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"schema_version":1`) {
		t.Fatal("valid receipt rejected", code, stdout.String(), stderr.String())
	}
}

func TestRunRejectsInvalidApprovedInstallReceipt(t *testing.T) {
	failed := func(string, string) (releasepack.ApprovedInstallVerificationReceipt, error) {
		return releasepack.ApprovedInstallVerificationReceipt{}, errors.New("denied")
	}
	for name, args := range map[string][]string{
		"missing": {"--record", "/evidence/approved-install.json"},
		"extra":   {"--record", "record", "--record-sha256", "digest", "extra"},
		"failed":  {"--record", "record", "--record-sha256", "digest"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr, failed); code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatal("invalid receipt accepted", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunHandlesHelpOutputFailureAndNilDependencies(t *testing.T) {
	verified := func(string, string) (releasepack.ApprovedInstallVerificationReceipt, error) {
		return releasepack.ApprovedInstallVerificationReceipt{SchemaVersion: 1}, nil
	}
	if run([]string{"--help"}, io.Discard, io.Discard, verified) != 0 ||
		run([]string{"--record", "r", "--record-sha256", "d"}, failingWriter{}, io.Discard, verified) != 1 ||
		run(nil, nil, io.Discard, verified) != 2 || run(nil, io.Discard, io.Discard, nil) != 2 {
		t.Fatal("CLI dependency boundary failed")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
