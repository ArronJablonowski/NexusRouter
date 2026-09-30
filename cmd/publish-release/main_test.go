package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/githubpublish"
	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

type unusedTransport struct{}

func (unusedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected network request")
}

func TestRunWiresFixedOriginsAndDeferredFDCredential(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, err = write.WriteString("github_pat_test-only\n"); err != nil || write.Close() != nil {
		t.Fatal(err)
	}
	called := 0
	publish := func(_ context.Context, config githubpublish.CredentialPublicationConfig, options releasepack.AuthorizedPublicationOptions) (githubpublish.PublicationEvidence, error) {
		called++
		if config.APIBase != githubAPIOrigin || config.UploadBase != githubUploadOrigin || options.JournalPath != "/outside/publication.jsonl" {
			t.Fatal("publication inputs were caller-overridable or lost")
		}
		credential, err := config.Secrets.GitHubCredential(t.Context())
		if err != nil || string(credential.Token) != "github_pat_test-only" || !credential.ContentsWrite || !credential.AdministrationRead {
			t.Fatal("credential FD was not deferred through the production seam", err)
		}
		clear(credential.Token)
		return githubpublish.PublicationEvidence{SchemaVersion: 1, Scope: "nexusrouter-github-authorized-publication", State: "confirmed_published", Published: true, Immutable: true}, nil
	}
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), validArgs(int(read.Fd())), &stdout, &stderr, unusedTransport{}, publish)
	if code != 0 || called != 1 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"state":"confirmed_published"`) || strings.Contains(stdout.String(), "github_pat") {
		t.Fatalf("unexpected result code=%d called=%d stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
}

func TestRunRejectsInvalidInvocationBeforePublisherOrCredentialRead(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, err = write.WriteString("must-remain-unread"); err != nil || write.Close() != nil {
		t.Fatal(err)
	}
	called := 0
	publish := func(context.Context, githubpublish.CredentialPublicationConfig, releasepack.AuthorizedPublicationOptions) (githubpublish.PublicationEvidence, error) {
		called++
		return githubpublish.PublicationEvidence{}, nil
	}
	args := validArgs(int(read.Fd()))
	args = args[:len(args)-4]
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), args, &stdout, &stderr, unusedTransport{}, publish); code != 2 || called != 0 || stdout.Len() != 0 {
		t.Fatalf("invalid input reached publisher: code=%d called=%d", code, called)
	}
	body, err := io.ReadAll(read)
	if err != nil || string(body) != "must-remain-unread" {
		t.Fatal("invalid invocation consumed credential input", err)
	}
}

func TestRunEmitsPublicUncertainEvidenceAndGenericFailure(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, err = write.WriteString("secret-value"); err != nil || write.Close() != nil {
		t.Fatal(err)
	}
	publish := func(context.Context, githubpublish.CredentialPublicationConfig, releasepack.AuthorizedPublicationOptions) (githubpublish.PublicationEvidence, error) {
		return githubpublish.PublicationEvidence{SchemaVersion: 1, Scope: "nexusrouter-github-authorized-publication", State: "uncertain", Phase: "upload_asset", RetryAllowed: false}, errors.New("github_pat_leak")
	}
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), validArgs(int(read.Fd())), &stdout, &stderr, unusedTransport{}, publish); code != 1 ||
		!strings.Contains(stdout.String(), `"state":"uncertain"`) || !strings.Contains(stderr.String(), "inspect the durable journal") ||
		strings.Contains(stdout.String()+stderr.String(), "github_pat_leak") {
		t.Fatalf("uncertain failure was not safely reported: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func validArgs(fd int) []string {
	digest := "sha256:" + strings.Repeat("a", 64)
	return []string{
		"--dir", "/release", "--source", "/source", "--candidate-record", "/candidate", "--candidate-record-sha256", digest,
		"--license-evidence", "/license", "--license-evidence-sha256", digest, "--expected-sums-sha256", digest,
		"--trust-record", "/trust", "--trust-record-sha256", digest, "--key-id", "release-key",
		"--key-fingerprint", digest, "--signing-authorization", "/signing", "--signing-authorization-sha256", digest,
		"--publication-authorization", "/publication", "--publication-authorization-sha256", digest,
		"--repository", "ArronJablonowski/NexusRouter", "--release-notes", "/notes", "--journal", "/outside/publication.jsonl",
		"--credential-fd", fmtInt(fd),
	}
}

func fmtInt(value int) string {
	return fmt.Sprintf("%d", value)
}
