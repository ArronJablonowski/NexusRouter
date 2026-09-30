package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Opt-in because this builds the entire clean, committed project eight times.
// It uses generated throwaway keys, never an operator's signing identity.
func TestReleaseQualification(t *testing.T) {
	if os.Getenv("DARWIN_RELEASE_QUALIFY") != "1" {
		t.Skip("set DARWIN_RELEASE_QUALIFY=1 on a clean committed checkout")
	}
	version, expectedCommit := os.Getenv("DARWIN_RELEASE_VERSION"), os.Getenv("DARWIN_RELEASE_COMMIT")
	if validate(Options{Version: version, Commit: expectedCommit, Out: "release"}) != nil {
		t.Fatal("provide a valid DARWIN_RELEASE_VERSION and DARWIN_RELEASE_COMMIT")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	source, err := command(ctx, ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := command(ctx, source, environment(), "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if commit != expectedCommit {
		t.Fatalf("source commit %q does not match DARWIN_RELEASE_COMMIT", commit)
	}
	t.Logf("qualifying release %s from source commit %s on %s/%s", version, commit, runtime.GOOS, runtime.GOARCH)
	parent := t.TempDir()
	first, second := filepath.Join(parent, "first"), filepath.Join(parent, "second")
	candidateFile := filepath.Join(parent, "candidate.json")
	if err = FreezeCandidate(ctx, Options{Version: version, Commit: commit, Out: candidateFile, Source: source}); err != nil {
		t.Fatal("candidate record", err)
	}
	candidateBody, err := os.ReadFile(candidateFile)
	if err != nil {
		t.Fatal(err)
	}
	licenseEvidenceFile := filepath.Join(parent, "license-evidence.json")
	licenseEvidenceSHA256, err := FreezeLicenseEvidence(ctx, LicenseEvidenceOptions{
		Commit: commit, Source: source, Out: licenseEvidenceFile,
	})
	if err != nil {
		t.Fatal("license evidence", err)
	}
	buildOutput, err := command(ctx, source, environment(), "go", "run", "./cmd/build-approved-release",
		"--out", second, "--source", source, "--candidate-record", candidateFile,
		"--candidate-record-sha256", prefixedDigest(candidateBody))
	if err != nil {
		t.Fatal("approved build CLI", err)
	}
	var buildResult ApprovedBuildResult
	if json.Unmarshal([]byte(buildOutput), &buildResult) != nil ||
		buildResult.CandidateRecordSHA256 != prefixedDigest(candidateBody) {
		t.Fatal("approved build identity")
	}
	if err = copyQualificationRelease(second, first); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(first)
	if err != nil || len(entries) != 6 {
		t.Fatalf("expected exactly four archives, manifest and sums: %v", err)
	}
	secondEntries, err := os.ReadDir(second)
	if err != nil || len(secondEntries) != len(entries) {
		t.Fatalf("second build has a different payload count: %v", err)
	}
	for _, entry := range entries {
		a, e := os.ReadFile(filepath.Join(first, entry.Name()))
		if e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(filepath.Join(second, entry.Name()))
		if e != nil || !bytes.Equal(a, b) {
			t.Fatalf("non-reproducible artifact %s: %v", entry.Name(), e)
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(private)
	seedFile, publicFile := filepath.Join(parent, "test-seed"), filepath.Join(parent, "test-public")
	if err = os.WriteFile(seedFile, []byte(hex.EncodeToString(private.Seed())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(publicFile, []byte(hex.EncodeToString(public)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	keyDigest := sha256.Sum256(public)
	keyFingerprint := "sha256:" + hex.EncodeToString(keyDigest[:])
	trustRecord := TrustRecord{
		SchemaVersion: 1, Project: "NexusRouter", Scope: trustScope, KeyID: "qualification-key",
		Algorithm: "Ed25519", PublicKey: hex.EncodeToString(public), PublicKeySHA256: keyFingerprint,
		Status: "active", PublishedAt: "2026-09-07T00:00:00Z",
		ReleasePolicyURL: "https://example.invalid/qualification-policy", RotationRevocationURL: "https://example.invalid/qualification-status",
	}
	trustBody, err := json.MarshalIndent(trustRecord, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	trustBody = append(trustBody, '\n')
	trustFile := filepath.Join(parent, "trust-record.json")
	if err = os.WriteFile(trustFile, trustBody, 0644); err != nil {
		t.Fatal(err)
	}
	sumsBody, err := os.ReadFile(filepath.Join(second, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if buildResult.SHA256SUMSSHA256 != prefixedDigest(sumsBody) {
		t.Fatal("approved build sums identity")
	}
	authorization := SigningAuthorization{
		SchemaVersion: signingAuthorizationSchema, Project: "NexusRouter", Scope: signingAuthorizationScope,
		CandidateRecordSHA256: prefixedDigest(candidateBody), LicenseEvidenceSHA256: licenseEvidenceSHA256,
		SHA256SUMSSHA256:  prefixedDigest(sumsBody),
		TrustRecordSHA256: prefixedDigest(trustBody), KeyID: trustRecord.KeyID, KeyFingerprint: keyFingerprint,
		Targets:    append([]SigningAuthorizationTarget(nil), authorizedTargets...),
		Gates:      append([]SigningAuthorizationGate(nil), signingAuthorizationGates...),
		ApproverID: "test:qualification-approver", ReleasePolicyURL: trustRecord.ReleasePolicyURL,
		ApprovedAt: "2026-09-07T00:01:00Z",
	}
	authorizationBody, err := json.MarshalIndent(authorization, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	authorizationBody = append(authorizationBody, '\n')
	authorizationFile := filepath.Join(parent, "authorization.json")
	if err = os.WriteFile(authorizationFile, authorizationBody, 0644); err != nil {
		t.Fatal(err)
	}
	if err = signUncheckedForTest(first, seedFile); err != nil {
		t.Fatal(err)
	}
	if err = Verify(first, publicFile); err != nil {
		t.Fatal(err)
	}
	if _, err = command(ctx, source, environment(), "go", "run", "./cmd/sign-release",
		"--dir", second, "--key", seedFile, "--candidate-record", candidateFile,
		"--candidate-record-sha256", prefixedDigest(candidateBody), "--source", source,
		"--license-evidence", licenseEvidenceFile, "--license-evidence-sha256", licenseEvidenceSHA256,
		"--expected-sums-sha256", prefixedDigest(sumsBody), "--trust-record", trustFile,
		"--trust-record-sha256", prefixedDigest(trustBody), "--key-id", trustRecord.KeyID,
		"--key-fingerprint", keyFingerprint, "--authorization-record", authorizationFile,
		"--authorization-record-sha256", prefixedDigest(authorizationBody)); err != nil {
		t.Fatal("signing CLI", err)
	}
	if _, err = command(ctx, source, environment(), "go", "run", "./cmd/verify-release", "--dir", second, "--public-key", publicFile); err != nil {
		t.Fatal("verification CLI", err)
	}
	verificationOutput, err := command(ctx, source, environment(), "go", "run", "./cmd/verify-approved-release",
		"--dir", second, "--source", source, "--candidate-record", candidateFile,
		"--candidate-record-sha256", prefixedDigest(candidateBody), "--expected-sums-sha256", prefixedDigest(sumsBody),
		"--license-evidence", licenseEvidenceFile, "--license-evidence-sha256", licenseEvidenceSHA256,
		"--trust-record", trustFile, "--trust-record-sha256", prefixedDigest(trustBody),
		"--key-id", trustRecord.KeyID, "--key-fingerprint", keyFingerprint,
		"--authorization-record", authorizationFile, "--authorization-record-sha256", prefixedDigest(authorizationBody))
	if err != nil {
		t.Fatal("approved verification CLI", err)
	}
	var verificationResult ApprovedVerificationResult
	if json.Unmarshal([]byte(verificationOutput), &verificationResult) != nil ||
		verificationResult.CandidateRecordSHA256 != prefixedDigest(candidateBody) ||
		verificationResult.LicenseEvidenceSHA256 != licenseEvidenceSHA256 ||
		verificationResult.SHA256SUMSSHA256 != prefixedDigest(sumsBody) ||
		verificationResult.TrustRecordSHA256 != prefixedDigest(trustBody) ||
		verificationResult.AuthorizationRecordSHA256 != prefixedDigest(authorizationBody) ||
		verificationResult.KeyID != trustRecord.KeyID || verificationResult.KeyFingerprint != keyFingerprint {
		t.Fatal("approved verification evidence")
	}
	firstSignature, err := os.ReadFile(filepath.Join(first, signatureName))
	if err != nil {
		t.Fatal(err)
	}
	secondSignature, err := os.ReadFile(filepath.Join(second, signatureName))
	if err != nil || !bytes.Equal(firstSignature, secondSignature) {
		t.Fatal("library/CLI signature mismatch", err)
	}
	body, err := os.ReadFile(filepath.Join(first, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err = json.Unmarshal(body, &manifest); err != nil || manifest.Version != version || manifest.Commit != commit {
		t.Fatalf("manifest identity: %v", err)
	}
	nativeRan := false
	for _, artifact := range manifest.Artifacts {
		binary := qualificationBinary(t, filepath.Join(first, artifact.File), artifact)
		assetDigest := assertEmbeddedWebUIAssets(t, source, binary)
		t.Logf("verified embedded WebUI assets sha256:%s in %s", assetDigest, artifact.File)
		if artifact.OS == "darwin" {
			f, e := macho.NewFile(bytes.NewReader(binary))
			want := macho.CpuAmd64
			if artifact.Arch == "arm64" {
				want = macho.CpuArm64
			}
			if e != nil || f.Cpu != want || f.Type != macho.TypeExec {
				t.Fatalf("Mach-O target mismatch: %s: %v", artifact.File, e)
			}
		} else {
			f, e := elf.NewFile(bytes.NewReader(binary))
			want := elf.EM_X86_64
			if artifact.Arch == "arm64" {
				want = elf.EM_AARCH64
			}
			if e != nil || f.Machine != want || f.Type != elf.ET_EXEC {
				t.Fatalf("ELF target mismatch: %s: %v", artifact.File, e)
			}
			for _, program := range f.Progs {
				if program.Type == elf.PT_INTERP {
					t.Fatalf("unexpected dynamic interpreter: %s", artifact.File)
				}
			}
		}
		if artifact.OS == runtime.GOOS && artifact.Arch == runtime.GOARCH {
			path := filepath.Join(parent, "darwin")
			if err = os.WriteFile(path, binary, 0700); err != nil {
				t.Fatal(err)
			}
			got, e := command(ctx, parent, environment(), path, "version")
			if e != nil || got != "nexus "+version {
				t.Fatalf("native version mismatch: %q: %v", got, e)
			}
			rehearsalRoot := t.TempDir()
			if err = os.Chmod(rehearsalRoot, 0700); err != nil {
				t.Fatal(err)
			}
			evidenceOut := os.Getenv("DARWIN_INSTALL_REHEARSAL_EVIDENCE_OUT")
			if evidenceOut == "" {
				evidenceOut = filepath.Join(rehearsalRoot, "install-rehearsal-evidence.json")
			}
			rehearseNativeInstallAndMigration(t, ctx, source, rehearsalRoot, filepath.Join(first, artifact.File), artifact, version, commit, evidenceOut)
			nativeRan = true
		}
	}
	if !nativeRan {
		t.Fatal("no native release target executed")
	}
	if err = os.WriteFile(filepath.Join(first, manifest.Artifacts[0].File), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if Verify(first, publicFile) == nil {
		t.Fatal("tampered release verified")
	}
	if _, err = command(ctx, source, environment(), "go", "run", "./cmd/verify-release", "--dir", first, "--public-key", publicFile); err == nil {
		t.Fatal("verification CLI accepted tampering")
	}
	t.Log("approved build CLI retained one of eight compared target builds: every unsigned byte including signed collateral and target-specific dependency notices matched; four executable formats checked; sign/raw and approval-bound verify CLIs exercised; ephemeral signatures matched; native install/migration/rollback rehearsal passed; tampering rejected")
}

func copyQualificationRelease(source, destination string) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		body, readErr := os.ReadFile(filepath.Join(source, entry.Name()))
		if readErr != nil {
			return readErr
		}
		if writeErr := os.WriteFile(filepath.Join(destination, entry.Name()), body, 0644); writeErr != nil {
			return writeErr
		}
	}
	return nil
}

func qualificationBinary(t *testing.T, path string, artifact Artifact) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	if len(artifact.Entries) != len(archiveContract) {
		t.Fatal("invalid manifest entry count")
	}
	var body []byte
	for i, contract := range archiveContract {
		h, err := tr.Next()
		if err != nil || !canonicalArchiveHeader(h, contract.name, int64(contract.mode), contract.max) {
			t.Fatalf("unexpected archive header: %v", err)
		}
		entry, err := io.ReadAll(io.LimitReader(tr, contract.max+1))
		if err != nil || int64(len(entry)) != h.Size || !validEntryMetadata(artifact.Entries[i], i, entry) {
			t.Fatal("archive member mismatch", contract.name, err)
		}
		if contract.name == noticeName && validateNotice(entry, artifact.OS, artifact.Arch) != nil {
			t.Fatal("invalid dependency notice")
		}
		if contract.name == "nexus" {
			body = entry
		}
	}
	if _, err = tr.Next(); err != io.EOF {
		t.Fatal("unexpected trailing archive entry", err)
	}
	return body
}
