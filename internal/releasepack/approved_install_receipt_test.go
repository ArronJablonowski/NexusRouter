package releasepack

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApprovedInstallVerificationOutputIsCreateOnlyAndVerifiable(t *testing.T) {
	options := approvedInstallFixture(t)
	receipt, err := VerifyApprovedInstall(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "approved-install.json")
	reservation, err := PrepareApprovedInstallVerificationOutput(out, options.Verification.Dir, options.Verification.Source, options.InstallRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	info, err := os.Lstat(out)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 0 {
		t.Fatal("receipt was not privately reserved", info, err)
	}
	if _, err = PrepareApprovedInstallVerificationOutput(out); err == nil {
		t.Fatal("existing reservation was reused")
	}
	digest, err := reservation.Commit(receipt)
	if err != nil || !trustFingerprint(digest) {
		t.Fatal("receipt commit failed", digest, err)
	}
	if _, err = reservation.Commit(receipt); err == nil {
		t.Fatal("receipt committed twice")
	}
	verified, err := VerifyApprovedInstallVerificationReceipt(out, digest)
	if err != nil || verified != receipt {
		t.Fatal("committed receipt did not verify", verified, err)
	}
	if _, err = VerifyApprovedInstallVerificationReceipt(out, testInstallDigest("f")); err == nil {
		t.Fatal("wrong independently supplied digest accepted")
	}
}

func TestApprovedInstallVerificationOutputRejectsUnsafePathsAndMutation(t *testing.T) {
	options := approvedInstallFixture(t)
	receipt, err := VerifyApprovedInstall(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []string{options.Verification.Dir, options.Verification.Source, options.InstallRoot} {
		if _, err = PrepareApprovedInstallVerificationOutput(filepath.Join(protected, "inside.json"), protected); err == nil {
			t.Fatal("receipt reserved in protected root", protected)
		}
	}
	writable := t.TempDir()
	if err = os.Chmod(writable, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareApprovedInstallVerificationOutput(filepath.Join(writable, "receipt.json")); err == nil {
		t.Fatal("world-writable output parent accepted")
	}
	realParent := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked")
	if err = os.Symlink(realParent, link); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareApprovedInstallVerificationOutput(filepath.Join(link, "receipt.json")); err == nil {
		t.Fatal("symlinked output parent accepted")
	}
	for _, scenario := range []string{"unlink", "replace", "append"} {
		t.Run(scenario, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "approved-install.json")
			reservation, prepareErr := PrepareApprovedInstallVerificationOutput(out)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			defer reservation.Close()
			switch scenario {
			case "unlink":
				if err := os.Remove(out); err != nil {
					t.Fatal(err)
				}
			case "replace":
				if err := os.Remove(out); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(out, []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			case "append":
				file, err := os.OpenFile(out, os.O_APPEND|os.O_WRONLY, 0)
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
			if _, err := reservation.Commit(receipt); err == nil {
				t.Fatal("mutated reservation committed")
			}
		})
	}
}

func TestIncompleteApprovedInstallVerificationReservationIsPreserved(t *testing.T) {
	out := filepath.Join(t.TempDir(), "approved-install.json")
	reservation, err := PrepareApprovedInstallVerificationOutput(out)
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
	if _, err = PrepareApprovedInstallVerificationOutput(out); err == nil {
		t.Fatal("incomplete reservation was silently reused")
	}
}
