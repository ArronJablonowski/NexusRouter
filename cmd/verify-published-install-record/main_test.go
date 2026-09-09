package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func TestRunVerifiesExactPublishedInstallRecord(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--record", "/evidence/install.json", "--record-sha256", digest}, &stdout, &stderr,
		func(path, gotDigest string) (releasepack.PublishedInstallEvidence, error) {
			if path != "/evidence/install.json" || gotDigest != digest {
				t.Fatal("verification inputs changed", path, gotDigest)
			}
			return releasepack.PublishedInstallEvidence{SchemaVersion: 1, Scope: "fixture"}, nil
		})
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"schema_version":1`) {
		t.Fatal("valid record rejected", code, stdout.String(), stderr.String())
	}
}

func TestRunRejectsInvalidPublishedInstallRecord(t *testing.T) {
	failed := func(string, string) (releasepack.PublishedInstallEvidence, error) {
		return releasepack.PublishedInstallEvidence{}, errors.New("denied")
	}
	for name, args := range map[string][]string{
		"missing": {"--record", "/evidence/install.json"},
		"extra":   {"--record", "/evidence/install.json", "--record-sha256", "digest", "extra"},
		"failed":  {"--record", "/evidence/install.json", "--record-sha256", "digest"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr, failed); code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatal("invalid record accepted", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunHandlesHelpParseOutputAndNilDependencies(t *testing.T) {
	verified := func(string, string) (releasepack.PublishedInstallEvidence, error) {
		return releasepack.PublishedInstallEvidence{SchemaVersion: 1}, nil
	}
	for name, tc := range map[string]struct {
		args   []string
		stdout io.Writer
		want   int
	}{
		"help":         {args: []string{"--help"}, stdout: io.Discard, want: 0},
		"parse":        {args: []string{"--unknown"}, stdout: io.Discard, want: 2},
		"output_error": {args: []string{"--record", "record.json", "--record-sha256", "sha256:" + strings.Repeat("a", 64)}, stdout: failingWriter{}, want: 1},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			if got := run(tc.args, tc.stdout, &stderr, verified); got != tc.want {
				t.Fatal("unexpected exit", got, tc.want, stderr.String())
			}
		})
	}
	if got := run(nil, nil, io.Discard, verified); got != 2 {
		t.Fatal("nil stdout accepted")
	}
	if got := run(nil, io.Discard, io.Discard, nil); got != 2 {
		t.Fatal("nil verifier accepted")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
