package releasepack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWritePublishedInstallEvidenceIsExclusiveAndProtected(t *testing.T) {
	receiptFile, installFile, expected := publishedInstallFixture(t)
	evidence, err := CreatePublishedInstallEvidence(context.Background(), receiptFile, installFile, expected)
	if err != nil {
		t.Fatal(err)
	}
	outRoot := t.TempDir()
	out := filepath.Join(outRoot, "published-install.json")
	digest, err := WritePublishedInstallEvidence(out, evidence, expected.DownloadDir, expected.InstallRoot)
	if err != nil || !trustFingerprint(digest) {
		t.Fatal("published install evidence not retained", digest, err)
	}
	info, err := os.Lstat(out)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("published install evidence permissions", info, err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyPublishedInstallReceipt(out, digest); err != nil {
		t.Fatal("retained evidence is not canonical", err)
	}
	var changed PublishedInstallEvidence
	if err = json.Unmarshal(body, &changed); err != nil {
		t.Fatal(err)
	}
	changed.InstalledBinarySHA256 = testInstallDigest("9")
	tampered, err := MarshalPublishedInstallEvidence(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out, tampered, 0600); err != nil {
		t.Fatal("cannot create tampered fixture", err)
	}
	if _, err = VerifyPublishedInstallReceipt(out, digest); err == nil {
		t.Fatal("retained evidence tampering accepted")
	}
	if _, err = WritePublishedInstallEvidence(out, evidence); err == nil {
		t.Fatal("existing evidence was overwritten")
	}
	if _, err = WritePublishedInstallEvidence(filepath.Join(expected.InstallRoot, "inside.json"), evidence, expected.InstallRoot); err == nil {
		t.Fatal("evidence was written inside protected install root")
	}
}

func TestWritePublishedInstallEvidenceRejectsSymlinkParent(t *testing.T) {
	receiptFile, installFile, expected := publishedInstallFixture(t)
	evidence, err := CreatePublishedInstallEvidence(context.Background(), receiptFile, installFile, expected)
	if err != nil {
		t.Fatal(err)
	}
	realParent := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked")
	if err = os.Symlink(realParent, link); err != nil {
		t.Fatal(err)
	}
	if _, err = WritePublishedInstallEvidence(filepath.Join(link, "evidence.json"), evidence); err == nil {
		t.Fatal("symlinked evidence parent accepted")
	}
}

func TestPreparePublishedInstallEvidenceOutputReservesBeforeCommit(t *testing.T) {
	receiptFile, installFile, expected := publishedInstallFixture(t)
	evidence, err := CreatePublishedInstallEvidence(context.Background(), receiptFile, installFile, expected)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "published-install.json")
	reservation, err := PreparePublishedInstallEvidenceOutput(out, expected.DownloadDir, expected.InstallRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	info, err := os.Lstat(out)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 0 {
		t.Fatal("output was not durably reserved", info, err)
	}
	if _, err = PreparePublishedInstallEvidenceOutput(out); err == nil {
		t.Fatal("existing reservation was reused")
	}
	digest, err := reservation.Commit(evidence)
	if err != nil || !trustFingerprint(digest) {
		t.Fatal("reservation commit failed", digest, err)
	}
	if _, err = reservation.Commit(evidence); err == nil {
		t.Fatal("reservation committed twice")
	}
	if _, err = VerifyPublishedInstallReceipt(out, digest); err != nil {
		t.Fatal("committed reservation is not verifiable", err)
	}
	for _, scenario := range []string{"unlink", "replace", "append"} {
		t.Run(scenario, func(t *testing.T) {
			mutatedOut := filepath.Join(t.TempDir(), "published-install.json")
			mutated, prepareErr := PreparePublishedInstallEvidenceOutput(mutatedOut, expected.DownloadDir, expected.InstallRoot)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			defer mutated.Close()
			switch scenario {
			case "unlink":
				if err := os.Remove(mutatedOut); err != nil {
					t.Fatal(err)
				}
			case "replace":
				if err := os.Remove(mutatedOut); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(mutatedOut, []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			case "append":
				file, err := os.OpenFile(mutatedOut, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = file.Write([]byte("x")); err != nil {
					file.Close()
					t.Fatal(err)
				}
				if err = file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := mutated.Commit(evidence); err == nil {
				t.Fatal("mutated reservation was committed")
			}
		})
	}
}

func TestPreparePublishedInstallEvidenceOutputRejectsUnsafeDestinations(t *testing.T) {
	private := t.TempDir()
	futureInstall := filepath.Join(private, "future-install")
	if _, err := PreparePublishedInstallEvidenceOutput(filepath.Join(futureInstall, "inside.json"), futureInstall); err == nil {
		t.Fatal("missing output parent accepted")
	}
	writable := t.TempDir()
	if err := os.Chmod(writable, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := PreparePublishedInstallEvidenceOutput(filepath.Join(writable, "evidence.json")); err == nil {
		t.Fatal("group/world-writable output parent accepted")
	}
}

func TestIncompletePublishedInstallReservationIsPreserved(t *testing.T) {
	out := filepath.Join(t.TempDir(), "published-install.json")
	reservation, err := PreparePublishedInstallEvidenceOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if err = reservation.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(out)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 0 {
		t.Fatal("incomplete reservation was not preserved", info, err)
	}
	if _, err = PreparePublishedInstallEvidenceOutput(out); err == nil {
		t.Fatal("incomplete reservation was silently reused")
	}
}
