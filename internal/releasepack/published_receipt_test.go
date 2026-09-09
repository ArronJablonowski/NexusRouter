package releasepack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritePostPublicationReceiptCreateOnlyAndProtected(t *testing.T) {
	receipt := validPublishedReceipt(t)
	parent := t.TempDir()
	out := filepath.Join(parent, "receipt.json")
	digest, err := WritePostPublicationReceipt(out, receipt)
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatal("canonical receipt not written", digest, err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := MarshalPostPublicationReceipt(receipt)
	if err != nil || string(body) != string(want) {
		t.Fatal("written receipt is not canonical", err)
	}
	if _, err = WritePostPublicationReceipt(out, receipt); err == nil {
		t.Fatal("existing receipt replaced")
	}
	protected := t.TempDir()
	if _, err = WritePostPublicationReceipt(filepath.Join(protected, "receipt.json"), receipt, protected); err == nil {
		t.Fatal("receipt written inside protected root")
	}
}

func TestWritePostPublicationReceiptRejectsSymlinkParent(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := WritePostPublicationReceipt(filepath.Join(link, "receipt.json"), validPublishedReceipt(t)); err == nil {
		t.Fatal("symlink parent accepted")
	}
}

func validPublishedReceipt(t *testing.T) PostPublicationReceipt {
	t.Helper()
	preflight, signedDir := publishedFixture(t)
	receipt, err := VerifyPublishedRelease(t.Context(), &fixtureReleaseReader{source: signedDir}, PublishedVerificationOptions{
		Preflight: preflight, DownloadDir: filepath.Join(t.TempDir(), "download"), VerifierID: "idp:release-verifier",
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
