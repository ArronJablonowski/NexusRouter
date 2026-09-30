package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/githubverify"
	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

type readerFixture struct{}

func (readerFixture) Verify(context.Context, githubverify.Plan) (githubverify.Observation, error) {
	return githubverify.Observation{}, errors.New("unused")
}

func fullArgs() []string {
	digest := "sha256:" + strings.Repeat("1", 64)
	return []string{
		"--dir", "/signed", "--source", "/source", "--candidate-record", "/candidate", "--candidate-record-sha256", digest,
		"--license-evidence", "/license", "--license-evidence-sha256", digest, "--expected-sums-sha256", digest,
		"--trust-record", "/trust", "--trust-record-sha256", digest, "--key-id", "release-1", "--key-fingerprint", digest,
		"--signing-authorization", "/signing", "--signing-authorization-sha256", digest,
		"--publication-authorization", "/publication", "--publication-authorization-sha256", digest,
		"--repository", "ArronJablonowski/NexusRouter", "--release-notes", "/notes",
		"--verifier-id", "idp:independent-release-verifier",
		"--download-dir", "/downloads", "--out", "/receipt",
		"--gh-binary", "/opt/reviewed/bin/gh", "--gh-binary-sha256", digest,
	}
}

func TestCLIForwardsAuthorityAndPersistsCanonicalReceipt(t *testing.T) {
	var got releasepack.PublishedVerificationOptions
	var protected []string
	var gotGHBinary, gotGHBinarySHA256 string
	digest := "sha256:" + strings.Repeat("2", 64)
	var out, diagnostic bytes.Buffer
	code := run(t.Context(), fullArgs(), &out, &diagnostic,
		func(binary, expectedSHA256 string) (releasepack.PublishedReleaseReader, error) {
			gotGHBinary, gotGHBinarySHA256 = binary, expectedSHA256
			return readerFixture{}, nil
		},
		func(_ context.Context, _ releasepack.PublishedReleaseReader, options releasepack.PublishedVerificationOptions) (releasepack.PostPublicationReceipt, error) {
			got = options
			return releasepack.PostPublicationReceipt{SchemaVersion: 2}, nil
		},
		func(path string, receipt releasepack.PostPublicationReceipt, roots ...string) (string, error) {
			if path != "/receipt" || receipt.SchemaVersion != 2 {
				t.Fatal("wrong receipt output", path, receipt)
			}
			protected = append([]string(nil), roots...)
			return digest, nil
		})
	if code != 0 || diagnostic.Len() != 0 || out.String() != digest+"\n" || got.DownloadDir != "/downloads" ||
		got.VerifierID != "idp:independent-release-verifier" ||
		gotGHBinary != "/opt/reviewed/bin/gh" || gotGHBinarySHA256 != "sha256:"+strings.Repeat("1", 64) ||
		got.Preflight.ExpectedRepository != "ArronJablonowski/NexusRouter" || got.Preflight.Verification.ExpectedKeyID != "release-1" ||
		len(protected) != 3 || protected[0] != "/source" || protected[1] != "/signed" || protected[2] != "/downloads" {
		t.Fatalf("authority not preserved: code=%d options=%+v protected=%v out=%q err=%q", code, got, protected, out.String(), diagnostic.String())
	}
}

func TestCLIRejectsMalformedVerifierIdentityBeforeNetwork(t *testing.T) {
	called := false
	args := fullArgs()
	for i := range args {
		if args[i] == "idp:independent-release-verifier" {
			args[i] = "INVALID VERIFIER"
		}
	}
	code := run(t.Context(), args, &bytes.Buffer{}, &bytes.Buffer{}, func(string, string) (releasepack.PublishedReleaseReader, error) {
		called = true
		return readerFixture{}, nil
	}, func(context.Context, releasepack.PublishedReleaseReader, releasepack.PublishedVerificationOptions) (releasepack.PostPublicationReceipt, error) {
		return releasepack.PostPublicationReceipt{}, nil
	}, func(string, releasepack.PostPublicationReceipt, ...string) (string, error) {
		return "", nil
	})
	if code != 2 || called {
		t.Fatal("malformed verifier identity reached network", code, called)
	}
}

func TestCLIRejectsMissingInputsBeforeNetwork(t *testing.T) {
	for _, missingFlag := range []string{"all", "--gh-binary", "--gh-binary-sha256"} {
		t.Run(missingFlag, func(t *testing.T) {
			args := fullArgs()
			if missingFlag == "all" {
				args = nil
			} else {
				for i := 0; i < len(args); i += 2 {
					if args[i] == missingFlag {
						args = append(args[:i], args[i+2:]...)
						break
					}
				}
			}
			called := false
			code := run(t.Context(), args, &bytes.Buffer{}, &bytes.Buffer{}, func(string, string) (releasepack.PublishedReleaseReader, error) {
				called = true
				return readerFixture{}, nil
			}, func(context.Context, releasepack.PublishedReleaseReader, releasepack.PublishedVerificationOptions) (releasepack.PostPublicationReceipt, error) {
				return releasepack.PostPublicationReceipt{}, nil
			}, func(string, releasepack.PostPublicationReceipt, ...string) (string, error) { return "", nil })
			if code != 2 || called {
				t.Fatal("missing input reached network", code, called)
			}
		})
	}
}

func TestCLIHidesAttestorConstructionFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), fullArgs(), &stdout, &stderr,
		func(string, string) (releasepack.PublishedReleaseReader, error) {
			return nil, errors.New("/secret/operator/path: gh emitted credential")
		}, func(context.Context, releasepack.PublishedReleaseReader, releasepack.PublishedVerificationOptions) (releasepack.PostPublicationReceipt, error) {
			t.Fatal("verification ran after attestor construction failure")
			return releasepack.PostPublicationReceipt{}, nil
		}, func(string, releasepack.PostPublicationReceipt, ...string) (string, error) {
			t.Fatal("receipt write ran after attestor construction failure")
			return "", nil
		})
	if code != 1 || stdout.Len() != 0 || stderr.String() != "published release verification failed\n" {
		t.Fatalf("unsafe construction diagnostic: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestCLIFailsClosedWithoutWritingReceipt(t *testing.T) {
	written := false
	code := run(t.Context(), fullArgs(), &bytes.Buffer{}, &bytes.Buffer{},
		func(string, string) (releasepack.PublishedReleaseReader, error) { return readerFixture{}, nil },
		func(context.Context, releasepack.PublishedReleaseReader, releasepack.PublishedVerificationOptions) (releasepack.PostPublicationReceipt, error) {
			return releasepack.PostPublicationReceipt{}, errors.New("remote mismatch")
		}, func(string, releasepack.PostPublicationReceipt, ...string) (string, error) {
			written = true
			return "", nil
		})
	if code != 1 || written {
		t.Fatal("failed verification persisted receipt", code, written)
	}
}
