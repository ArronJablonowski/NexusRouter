package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/githubverify"
	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
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
		"--repository", "ArronJablonowski/DarwinRouter", "--release-notes", "/notes",
		"--download-dir", "/downloads", "--out", "/receipt",
	}
}

func TestCLIForwardsAuthorityAndPersistsCanonicalReceipt(t *testing.T) {
	var got releasepack.PublishedVerificationOptions
	var protected []string
	digest := "sha256:" + strings.Repeat("2", 64)
	var out, diagnostic bytes.Buffer
	code := run(t.Context(), fullArgs(), &out, &diagnostic,
		func() (releasepack.PublishedReleaseReader, error) { return readerFixture{}, nil },
		func(_ context.Context, _ releasepack.PublishedReleaseReader, options releasepack.PublishedVerificationOptions) (releasepack.PostPublicationReceipt, error) {
			got = options
			return releasepack.PostPublicationReceipt{SchemaVersion: 1}, nil
		},
		func(path string, receipt releasepack.PostPublicationReceipt, roots ...string) (string, error) {
			if path != "/receipt" || receipt.SchemaVersion != 1 {
				t.Fatal("wrong receipt output", path, receipt)
			}
			protected = append([]string(nil), roots...)
			return digest, nil
		})
	if code != 0 || diagnostic.Len() != 0 || out.String() != digest+"\n" || got.DownloadDir != "/downloads" ||
		got.Preflight.ExpectedRepository != "ArronJablonowski/DarwinRouter" || got.Preflight.Verification.ExpectedKeyID != "release-1" ||
		len(protected) != 3 || protected[0] != "/source" || protected[1] != "/signed" || protected[2] != "/downloads" {
		t.Fatalf("authority not preserved: code=%d options=%+v protected=%v out=%q err=%q", code, got, protected, out.String(), diagnostic.String())
	}
}

func TestCLIRejectsMissingInputsBeforeNetwork(t *testing.T) {
	called := false
	code := run(t.Context(), nil, &bytes.Buffer{}, &bytes.Buffer{}, func() (releasepack.PublishedReleaseReader, error) {
		called = true
		return readerFixture{}, nil
	}, nil, nil)
	if code != 2 || called {
		t.Fatal("missing input reached network", code, called)
	}
}

func TestCLIFailsClosedWithoutWritingReceipt(t *testing.T) {
	written := false
	code := run(t.Context(), fullArgs(), &bytes.Buffer{}, &bytes.Buffer{},
		func() (releasepack.PublishedReleaseReader, error) { return readerFixture{}, nil },
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
