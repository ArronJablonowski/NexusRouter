package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func TestRunBindsPublishedInstallInputs(t *testing.T) {
	args := validArgs()
	wantTime := time.Date(2026, 9, 8, 17, 0, 0, 0, time.UTC)
	clockCalls, preflighted := 0, false
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr, func() time.Time { clockCalls++; return wantTime },
		func(_ context.Context, receipt, install string, got releasepack.PublishedInstallExpectations) (releasepack.PublishedInstallEvidence, error) {
			if !preflighted || clockCalls != 0 || receipt != args[1] || install != args[5] ||
				got.PublicationReceiptSHA256 != args[3] || got.InstallEvidenceSHA256 != args[7] || got.BackupSHA256 != args[9] ||
				got.TargetOS != args[11] || got.TargetArch != args[13] || got.VerifierID != args[15] ||
				got.DownloadDir != args[17] || got.InstallRoot != args[19] || got.Now == nil || !got.Now().Equal(wantTime) {
				t.Fatalf("unexpected verification inputs: %q %q %#v", receipt, install, got)
			}
			return releasepack.PublishedInstallEvidence{SchemaVersion: 1}, nil
		},
		func(path string, roots ...string) (evidenceOutput, error) {
			if path != args[21] || strings.Join(roots, "|") != args[17]+"|"+args[19] {
				t.Fatalf("unexpected persistence inputs: %q %#v", path, roots)
			}
			preflighted = true
			return &stubEvidenceOutput{commit: func(evidence releasepack.PublishedInstallEvidence) (string, error) {
				if evidence.SchemaVersion != 1 || clockCalls != 1 {
					t.Fatalf("unexpected committed evidence: %#v", evidence)
				}
				return "sha256:" + string(bytes.Repeat([]byte("a"), 64)), nil
			}}, nil
		})
	if code != 0 || stderr.Len() != 0 || stdout.String() != "sha256:"+string(bytes.Repeat([]byte("a"), 64))+"\n" {
		t.Fatal("successful verification rejected", code, stdout.String(), stderr.String())
	}
}

func TestRunRejectsMissingOrFailedPublishedInstallEvidence(t *testing.T) {
	verify := func(context.Context, string, string, releasepack.PublishedInstallExpectations) (releasepack.PublishedInstallEvidence, error) {
		return releasepack.PublishedInstallEvidence{}, errors.New("denied")
	}
	prepare := func(string, ...string) (evidenceOutput, error) { return &stubEvidenceOutput{}, nil }
	for name, args := range map[string][]string{"missing": validArgs()[:20], "extra": append(validArgs(), "extra"), "failed": validArgs()} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), args, &stdout, &stderr, time.Now, verify, prepare)
			if code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatal("invalid invocation accepted", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunRejectsPreflightCommitAndOutputFailures(t *testing.T) {
	verifyCalls := 0
	verify := func(context.Context, string, string, releasepack.PublishedInstallExpectations) (releasepack.PublishedInstallEvidence, error) {
		verifyCalls++
		return releasepack.PublishedInstallEvidence{SchemaVersion: 1}, nil
	}
	tests := map[string]struct {
		stdout  io.Writer
		prepare preparer
	}{
		"preflight":  {stdout: io.Discard, prepare: func(string, ...string) (evidenceOutput, error) { return nil, errors.New("denied") }},
		"nil_output": {stdout: io.Discard, prepare: func(string, ...string) (evidenceOutput, error) { return nil, nil }},
		"commit": {stdout: io.Discard, prepare: func(string, ...string) (evidenceOutput, error) {
			return &stubEvidenceOutput{commit: func(releasepack.PublishedInstallEvidence) (string, error) { return "", errors.New("denied") }}, nil
		}},
		"stdout": {stdout: failingWriter{}, prepare: func(string, ...string) (evidenceOutput, error) {
			return &stubEvidenceOutput{commit: func(releasepack.PublishedInstallEvidence) (string, error) {
				return "sha256:" + strings.Repeat("a", 64), nil
			}}, nil
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			before := verifyCalls
			var stderr bytes.Buffer
			if code := run(context.Background(), validArgs(), tc.stdout, &stderr, time.Now, verify, tc.prepare); code == 0 || stderr.Len() == 0 {
				t.Fatal("failed operation accepted", code, stderr.String())
			}
			if (name == "preflight" || name == "nil_output") && verifyCalls != before {
				t.Fatal("native verification ran before output preflight")
			}
		})
	}
}

func TestRunHandlesHelpParseAndNilDependencies(t *testing.T) {
	prepare := func(string, ...string) (evidenceOutput, error) { return &stubEvidenceOutput{}, nil }
	verify := func(context.Context, string, string, releasepack.PublishedInstallExpectations) (releasepack.PublishedInstallEvidence, error) {
		return releasepack.PublishedInstallEvidence{}, nil
	}
	for name, tc := range map[string]struct {
		args []string
		want int
	}{
		"help":  {[]string{"--help"}, 0},
		"parse": {[]string{"--unknown"}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(context.Background(), tc.args, &stdout, &stderr, time.Now, verify, prepare); got != tc.want {
				t.Fatal("unexpected exit", got, tc.want)
			}
		})
	}
	if got := run(nil, nil, io.Discard, io.Discard, time.Now, verify, prepare); got != 2 {
		t.Fatal("nil context accepted")
	}
	if got := run(context.Background(), nil, nil, io.Discard, time.Now, verify, prepare); got != 2 {
		t.Fatal("nil output accepted")
	}
}

type stubEvidenceOutput struct {
	commit func(releasepack.PublishedInstallEvidence) (string, error)
	closed bool
}

func (s *stubEvidenceOutput) Commit(e releasepack.PublishedInstallEvidence) (string, error) {
	if s.commit == nil {
		return "", nil
	}
	return s.commit(e)
}

func (s *stubEvidenceOutput) Close() error { s.closed = true; return nil }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func validArgs() []string {
	digest := "sha256:" + string(bytes.Repeat([]byte("a"), 64))
	return []string{
		"--publication-receipt", "/evidence/publication.json", "--publication-receipt-sha256", digest,
		"--install-evidence", "/evidence/install.json", "--install-evidence-sha256", digest,
		"--backup-sha256", digest, "--target-os", "darwin", "--target-arch", "arm64",
		"--verifier-id", "idp:published-install-verifier", "--download-dir", "/downloads/release",
		"--install-root", "/install/release", "--out", "/evidence/published-install.json",
	}
}
