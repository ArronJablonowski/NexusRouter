package releasepack

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestApprovedSigningRoundTrip(t *testing.T) {
	options, public := approvedSigningIntegrationFixture(t)
	if err := SignApproved(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTrustRecord(options.Dir, options.TrustRecordFile, options.ExpectedKeyID,
		options.ExpectedKeyFingerprint, options.ExpectedTrustRecordSHA256); err != nil {
		t.Fatal(err)
	}
	signature, err := os.ReadFile(filepath.Join(options.Dir, signatureName))
	if err != nil || len(signature) != 2*ed25519.SignatureSize+1 {
		t.Fatal("signature output", err)
	}
	if err = SignApproved(context.Background(), options); err != ErrSignature {
		t.Fatal("existing production signature overwritten", err)
	}
	if len(public) != ed25519.PublicKeySize {
		t.Fatal("fixture public key")
	}
}

func TestApprovedSigningPreflightDoesNotReadKey(t *testing.T) {
	for _, scenario := range []string{
		"candidate", "candidate_identity", "license_evidence", "license_evidence_missing", "license_evidence_swapped",
		"authorization", "policy_mismatch", "trust", "sums",
		"artifact", "existing_signature", "source", "key_in_source", "key_in_release", "canceled",
	} {
		t.Run(scenario, func(t *testing.T) {
			options, _ := approvedSigningFastFixture(t)
			ctx := context.Background()
			switch scenario {
			case "candidate":
				options.ExpectedCandidateSHA256 = "sha256:" + strings.Repeat("0", 64)
			case "candidate_identity":
				alternate := filepath.Join(t.TempDir(), "alternate-candidate.json")
				commit := approvedSourceCommit(t, options.Source)
				if err := FreezeCandidate(ctx, Options{Version: "1.0.1", Commit: commit, Out: alternate, Source: options.Source}); err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(alternate)
				if err != nil {
					t.Fatal(err)
				}
				options.CandidateRecordFile, options.ExpectedCandidateSHA256 = alternate, prefixedDigest(body)
			case "license_evidence":
				options.ExpectedLicenseEvidenceSHA256 = "sha256:" + strings.Repeat("0", 64)
			case "license_evidence_missing":
				options.LicenseEvidenceFile = filepath.Join(t.TempDir(), "missing-license-evidence.json")
			case "license_evidence_swapped":
				options.LicenseEvidenceFile = options.CandidateRecordFile
				options.ExpectedLicenseEvidenceSHA256 = options.ExpectedCandidateSHA256
			case "authorization":
				options.ExpectedAuthorizationSHA256 = "sha256:" + strings.Repeat("0", 64)
			case "policy_mismatch":
				body, authorization := readAuthorizationFixture(t, options.AuthorizationRecordFile)
				authorization.ReleasePolicyURL = "https://example.invalid/different-policy"
				body = canonicalAuthorizationFixture(t, authorization)
				writeSigningFixture(t, options.AuthorizationRecordFile, body, 0644)
				options.ExpectedAuthorizationSHA256 = prefixedDigest(body)
			case "trust":
				options.ExpectedTrustRecordSHA256 = "sha256:" + strings.Repeat("0", 64)
			case "sums":
				options.ExpectedSumsSHA256 = "sha256:" + strings.Repeat("0", 64)
			case "artifact":
				path := filepath.Join(options.Dir, "DarwinRouter_1.0.0_darwin_amd64.tar.gz")
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, append(body, 'x'), 0644); err != nil {
					t.Fatal(err)
				}
			case "existing_signature":
				writeSigningFixture(t, filepath.Join(options.Dir, signatureName), []byte(strings.Repeat("0", 128)+"\n"), 0644)
			case "source":
				if err := os.WriteFile(filepath.Join(options.Source, "untracked"), []byte("dirty\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "key_in_source":
				options.KeyFile = filepath.Join(options.Source, "go.mod")
			case "key_in_release":
				options.KeyFile = filepath.Join(options.Dir, "manifest.json")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			calls := 0
			reader := func(string) ([]byte, error) {
				calls++
				return make([]byte, ed25519.SeedSize), nil
			}
			if err := signApprovedFixture(ctx, options, reader); err != ErrSignature {
				t.Fatal("failed preflight accepted", err)
			}
			if calls != 0 {
				t.Fatalf("failed %s preflight invoked private-key reader", scenario)
			}
			if scenario != "existing_signature" {
				if _, err := os.Lstat(filepath.Join(options.Dir, signatureName)); !os.IsNotExist(err) {
					t.Fatal("failed preflight created a signature", err)
				}
			}
		})
	}
}

func TestApprovedSigningRejectsWrongSeed(t *testing.T) {
	options, _ := approvedSigningFastFixture(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(other)
	if err = os.WriteFile(options.KeyFile, []byte(hex.EncodeToString(other.Seed())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = signApprovedFixture(context.Background(), options, func(path string) ([]byte, error) { return signingKeyFile(path, true) }); err != ErrSignature {
		t.Fatal("wrong private identity accepted", err)
	}
	if _, err = os.Lstat(filepath.Join(options.Dir, signatureName)); !os.IsNotExist(err) {
		t.Fatal("failed preflight created a signature", err)
	}
}

func TestApprovedSigningReadsPrivateKeyOnceAfterPreflight(t *testing.T) {
	options, _ := approvedSigningFastFixture(t)
	seed, err := signingKeyFile(options.KeyFile, true)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(seed)
	calls := 0
	reader := func(path string) ([]byte, error) {
		calls++
		if path != options.KeyFile {
			t.Fatalf("unexpected private-key path %q", path)
		}
		return append([]byte(nil), seed...), nil
	}
	if err = signApprovedFixture(context.Background(), options, reader); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("private-key reader called %d times", calls)
	}
}

func TestPrivateKeyOutsideRootsResolvesParentSymlinks(t *testing.T) {
	root := t.TempDir()
	keyDir := filepath.Join(root, "keys")
	if err := os.Mkdir(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(keyDir, "seed")
	writeSigningFixture(t, keyFile, []byte(strings.Repeat("0", 64)+"\n"), 0600)
	alias := filepath.Join(t.TempDir(), "source-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if privateKeyOutsideRoots(filepath.Join(alias, "keys", "seed"), root) {
		t.Fatal("key reached through a parent symlink was accepted inside protected root")
	}
	external := filepath.Join(t.TempDir(), "seed")
	writeSigningFixture(t, external, []byte(strings.Repeat("0", 64)+"\n"), 0600)
	if !privateKeyOutsideRoots(external, root) {
		t.Fatal("key outside protected root was rejected")
	}
}

func TestApprovedSigningRejectsArtifactNoticeOutsideLicenseEvidenceBeforeKey(t *testing.T) {
	options, _ := approvedSigningFixtureWithExecutableVersionAndEvidence(t, true, "", "", false)
	calls := 0
	reader := func(string) ([]byte, error) {
		calls++
		return make([]byte, ed25519.SeedSize), nil
	}
	if err := signApprovedFixture(context.Background(), options, reader); err != ErrSignature {
		t.Fatal("artifact notice outside license evidence accepted", err)
	}
	if calls != 0 {
		t.Fatal("artifact/evidence mismatch reached private-key reader")
	}
}

func TestApprovedSigningRejectsSBOMIdentityDriftBeforeKey(t *testing.T) {
	for _, drift := range []string{"missing", "tampered", "created", "target", "binary", "module", "toolchain", "source_asset"} {
		t.Run(drift, func(t *testing.T) {
			options, _ := approvedSigningFixtureWithExecutableVersionAndEvidence(t, false, "", drift, false)
			calls := 0
			reader := func(string) ([]byte, error) {
				calls++
				return make([]byte, ed25519.SeedSize), nil
			}
			if err := signApprovedFixture(context.Background(), options, reader); err != ErrSignature {
				t.Fatal("SBOM identity drift accepted", err)
			}
			if calls != 0 {
				t.Fatal("SBOM identity drift reached private-key reader")
			}
		})
	}
}

func TestApprovedArtifactLicenseIdentityRejectsRelationalMismatch(t *testing.T) {
	options, _ := approvedSigningFixture(t)
	_, evidence, err := readLicenseEvidence(options.LicenseEvidenceFile)
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := checkedRelease(options.Dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	assets, err := readSBOMSourceFiles(options.Source)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*LicenseEvidence){
		"source_commit": func(record *LicenseEvidence) { record.SourceCommit = strings.Repeat("a", 40) },
		"toolchain":     func(record *LicenseEvidence) { record.Toolchain.GOVERSION = "go1.99.0" },
		"notice_digest": func(record *LicenseEvidence) { record.Targets[0].NoticeSHA256 = invalidPublicDigest() },
		"target_swap": func(record *LicenseEvidence) {
			record.Targets[0], record.Targets[1] = record.Targets[1], record.Targets[0]
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneLicenseEvidence(evidence)
			mutate(&changed)
			candidate := candidateFixtureFromFile(t, options.CandidateRecordFile)
			if approvedArtifactLicenseIdentity(root, candidate, changed, assets, candidate.ReleaseCreated) == nil {
				t.Fatal("artifact accepted with mismatched license evidence")
			}
		})
	}
}

func candidateFixtureFromFile(t *testing.T, path string) CandidateRecord {
	t.Helper()
	_, candidate, err := readCandidateRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func readAuthorizationFixture(t *testing.T, path string) ([]byte, SigningAuthorization) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var authorization SigningAuthorization
	if err = json.Unmarshal(body, &authorization); err != nil {
		t.Fatal(err)
	}
	return body, authorization
}

func canonicalAuthorizationFixture(t *testing.T, authorization SigningAuthorization) []byte {
	t.Helper()
	body, err := json.MarshalIndent(authorization, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func approvedSigningFixture(t *testing.T) (ApprovedSigningOptions, ed25519.PublicKey) {
	return approvedSigningFixtureWithExecutableVersionAndEvidence(t, false, "", "", true)
}

func approvedSigningIntegrationFixture(t *testing.T) (ApprovedSigningOptions, ed25519.PublicKey) {
	return approvedSigningFixtureWithExecutableVersionAndEvidence(t, false, "", "", true)
}

func approvedSigningFastFixture(t *testing.T) (ApprovedSigningOptions, ed25519.PublicKey) {
	return approvedSigningFixtureWithExecutableVersionAndEvidence(t, false, "", "", false)
}

func signApprovedFixture(ctx context.Context, options ApprovedSigningOptions, readKey privateKeyReader) error {
	return signApprovedWithLicenseEvidenceVerifier(ctx, options, readKey, fixtureLicenseEvidenceVerifier(options.Dir))
}

// fixtureLicenseEvidenceVerifier retains the canonical-byte, digest, checkout,
// commit, and protected-path gates. Higher-level tests may skip only the fresh
// four-target reconstruction that has dedicated integration coverage.
func fixtureLicenseEvidenceVerifier(expectedProtected ...string) licenseEvidenceRecordVerifier {
	want := make([]string, len(expectedProtected))
	for i, path := range expectedProtected {
		want[i], _ = canonicalProspectivePath(path)
	}
	return func(ctx context.Context, recordPath, expectedSHA256, source string, protectedPaths ...string) (LicenseEvidence, error) {
		if ctx == nil || ctx.Err() != nil || !trustFingerprint(expectedSHA256) || len(protectedPaths) != len(want) {
			return LicenseEvidence{}, ErrInvalid
		}
		for i, path := range protectedPaths {
			got, err := canonicalProspectivePath(path)
			if err != nil || want[i] == "" || got != want[i] {
				return LicenseEvidence{}, ErrInvalid
			}
		}
		body, record, err := readLicenseEvidence(recordPath)
		if err != nil || licenseEvidenceDigest(body) != expectedSHA256 {
			return LicenseEvidence{}, ErrInvalid
		}
		root, err := filepath.Abs(source)
		if err != nil {
			return LicenseEvidence{}, ErrInvalid
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil || verifyCandidateCheckout(ctx, root, record.SourceCommit, environment()) != nil {
			return LicenseEvidence{}, ErrInvalid
		}
		return record, nil
	}
}

func approvedSigningFixtureWithNoticeMismatch(t *testing.T) (ApprovedSigningOptions, ed25519.PublicKey) {
	return approvedSigningFixtureWithOptions(t, true, false, "")
}

func approvedSigningExecutableFixture(t *testing.T) (ApprovedSigningOptions, ed25519.PublicKey) {
	return approvedSigningFixtureWithOptions(t, false, true, "")
}

func approvedSigningFixtureWithOptions(t *testing.T, mismatchNotice, executableNative bool, sbomDrift string) (ApprovedSigningOptions, ed25519.PublicKey) {
	executableVersion := ""
	if executableNative {
		executableVersion = "1.0.0"
	}
	return approvedSigningFixtureWithExecutableVersion(t, mismatchNotice, executableVersion, sbomDrift)
}

func approvedSigningFixtureWithExecutableVersion(t *testing.T, mismatchNotice bool, executableVersion, sbomDrift string) (ApprovedSigningOptions, ed25519.PublicKey) {
	return approvedSigningFixtureWithExecutableVersionAndEvidence(t, mismatchNotice, executableVersion, sbomDrift, true)
}

func approvedSigningFixtureWithExecutableVersionAndEvidence(t *testing.T, mismatchNotice bool, executableVersion, sbomDrift string, reconstructEvidence bool) (ApprovedSigningOptions, ed25519.PublicKey) {
	t.Helper()
	ctx := context.Background()
	source := collateralSourceFixture(t)
	var err error
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/approved\n\ngo 1.27.1\n\nrequire github.com/google/uuid v1.6.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(source, "cmd", "darwin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(source, "webui", "assets", "v1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "webui", "assets", "v1", "app.js"), []byte("fixture frontend source\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "webui", "shell.go"), []byte("package webui\n\nimport \"embed\"\n\n//go:embed assets/v1/*\nvar assets embed.FS\n"), 0644); err != nil {
		t.Fatal(err)
	}
	program := "package main\n\nimport _ \"github.com/google/uuid\"\n\nfunc main() {}\n"
	if executableVersion != "" {
		program = "package main\n\nimport (\n  \"fmt\"\n  _ \"github.com/google/uuid\"\n  \"os\"\n)\n\nfunc main() {\n  if len(os.Args) == 2 && os.Args[1] == \"version\" {\n    fmt.Println(\"darwin " + executableVersion + "\")\n    return\n  }\n  os.Exit(2)\n}\n"
	}
	if err = os.WriteFile(filepath.Join(source, "cmd", "darwin", "main.go"), []byte(program), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = command(ctx, source, environment(), "go", "mod", "tidy"); err != nil {
		t.Fatal(err)
	}
	for _, step := range [][]string{{"init"}, {"config", "user.email", "approved@example.invalid"}, {"config", "user.name", "Approved Test"}, {"add", "."}, {"commit", "-m", "fixture"}} {
		if _, err = command(ctx, source, environment(), "git", step...); err != nil {
			t.Fatal(step, err)
		}
	}
	commit := approvedSourceCommit(t, source)
	created, err := releaseCommitCreated(ctx, source, commit, environment())
	if err != nil {
		t.Fatal(err)
	}
	toolchainEvidence, err := licenseEvidenceToolchain(runtime.Version(), "1.27.1")
	if err != nil {
		t.Fatal(err)
	}
	goMod, err := os.ReadFile(filepath.Join(source, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goSum, err := os.ReadFile(filepath.Join(source, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	rootLicense, err := os.ReadFile(filepath.Join(source, licenseName))
	if err != nil {
		t.Fatal(err)
	}
	licenseEvidence := LicenseEvidence{
		SchemaVersion: licenseEvidenceSchema, Scope: licenseEvidenceScope, SourceCommit: commit,
		SourceInputs: LicenseEvidenceSourceInputs{GoModSHA256: licenseEvidenceDigest(goMod), GoSumSHA256: licenseEvidenceDigest(goSum)},
		Reconstruction: LicenseEvidenceReconstruction{
			Policy: goReconstructionPolicy, ModuleProxy: goReconstructionProxy, ChecksumDatabase: goReconstructionSumDatabase,
			ModuleMode: "readonly", ModuleCache: "fresh-isolated", BuildCache: "fresh-isolated",
			NetworkFallback: "disabled", PrivateModules: "disabled",
		},
		Toolchain:   toolchainEvidence,
		RootLicense: LicenseEvidenceRoot{Source: licenseName, SPDX: "MIT", Size: int64(len(rootLicense)), SHA256: licenseEvidenceDigest(rootLicense)},
	}
	candidateFile := filepath.Join(t.TempDir(), "candidate.json")
	if err = FreezeCandidate(ctx, Options{Version: "1.0.0", Commit: commit, Out: candidateFile, Source: source}); err != nil {
		t.Fatal(err)
	}
	candidateBody, err := os.ReadFile(candidateFile)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := loadCollateral(source)
	if err != nil {
		t.Fatal(err)
	}
	releaseDir := t.TempDir()
	assets, err := readSBOMSourceFiles(source)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: releaseManifestSchema, Version: "1.0.0", Commit: commit, Created: created, Toolchain: toolchainEvidence.GOVERSION}
	var sums strings.Builder
	for _, target := range [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		name := "DarwinRouter_1.0.0_" + target[0] + "_" + target[1] + ".tar.gz"
		modules, entryErr := targetNoticeModules(ctx, source, target[0], target[1], environment())
		if entryErr != nil {
			t.Fatal(entryErr)
		}
		notice, entryErr := renderThirdPartyNotices(target[0], target[1], modules)
		if entryErr != nil {
			t.Fatal(entryErr)
		}
		evidenceTarget := LicenseEvidenceTarget{
			OS: target[0], Arch: target[1], PackageCount: 1,
			DependencyGraphSHA256: licenseEvidenceDigest([]byte(target[0] + "/" + target[1])),
			NoticeSHA256:          licenseEvidenceDigest(notice),
		}
		for _, module := range modules {
			evidenceModule := LicenseEvidenceModule{Path: module.Path, Version: module.Version, Sum: module.Sum, GoModSum: module.GoModSum}
			if module.Path == "github.com/google/uuid" {
				evidenceModule.Sum = "h1:NIvaJDMOsjHA8n1jAhLSgzrAzy1Hgr+hNrb57e+94F0="
				evidenceModule.GoModSum = "h1:TIyPZe4MgqvfeYDBFedMoGGpEw/LqOeaOT+nhxU+yHo="
			}
			for _, file := range module.Files {
				evidenceModule.Files = append(evidenceModule.Files, LicenseEvidenceFile{Name: file.Name, Size: int64(len(file.Body)), SHA256: licenseEvidenceDigest(file.Body)})
			}
			evidenceTarget.Modules = append(evidenceTarget.Modules, evidenceModule)
		}
		licenseEvidence.Targets = append(licenseEvidence.Targets, evidenceTarget)
		if mismatchNotice && target[0] == "darwin" && target[1] == "amd64" {
			notice = signingNoticeFixture(target[0], target[1])
		}
		binary := signingBinaryFixture(t, target[0], target[1])
		if executableVersion != "" && target[0] == runtime.GOOS && target[1] == runtime.GOARCH {
			binaryPath := filepath.Join(t.TempDir(), "darwin")
			buildEnv := append(environment(), "GOOS="+target[0], "GOARCH="+target[1])
			if _, entryErr = command(ctx, source, buildEnv, "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=", "-o", binaryPath, "./cmd/darwin"); entryErr != nil {
				t.Fatal(entryErr)
			}
			binary, entryErr = os.ReadFile(binaryPath)
			if entryErr != nil {
				t.Fatal(entryErr)
			}
		}
		binaryDigest := sha256.Sum256(binary)
		sbom, entryErr := renderTargetSBOM(TargetSBOMOptions{
			Version: "1.0.0", Commit: commit, TargetOS: target[0], TargetArch: target[1],
			Created: created, BinarySHA256: hex.EncodeToString(binaryDigest[:]),
		}, modules, assets)
		if entryErr != nil {
			t.Fatal(entryErr)
		}
		if sbomDrift != "" && target[0] == "darwin" && target[1] == "amd64" {
			if sbomDrift == "tampered" {
				sbom = append([]byte(nil), sbom...)
				sbom[len(sbom)-2] ^= 1
			} else if sbomDrift != "missing" {
				sbom = driftApprovedSBOMFixture(t, sbom, sbomDrift)
			}
		}
		entries, metadata, entryErr := releaseEntries(shared, notice, sbom, binary)
		if entryErr != nil {
			t.Fatal(entryErr)
		}
		if sbomDrift == "missing" && target[0] == "darwin" && target[1] == "amd64" {
			entries = append(entries[:3], entries[4:]...)
			metadata = append(metadata[:3], metadata[4:]...)
		}
		var archive strings.Builder
		if entryErr = Archive(&archive, entries); entryErr != nil {
			t.Fatal(entryErr)
		}
		body := []byte(archive.String())
		writeSigningFixture(t, filepath.Join(releaseDir, name), body, 0644)
		digest := sha256.Sum256(body)
		hash := hex.EncodeToString(digest[:])
		fmt.Fprintf(&sums, "%s  %s\n", hash, name)
		manifest.Artifacts = append(manifest.Artifacts, Artifact{OS: target[0], Arch: target[1], File: name, SHA256: hash, Entries: metadata})
	}
	manifestBody, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestBody = append(manifestBody, '\n')
	writeSigningFixture(t, filepath.Join(releaseDir, "manifest.json"), manifestBody, 0644)
	fmt.Fprintf(&sums, "%x  manifest.json\n", sha256.Sum256(manifestBody))
	sumsBody := []byte(sums.String())
	writeSigningFixture(t, filepath.Join(releaseDir, "SHA256SUMS"), sumsBody, 0644)
	licenseEvidenceFile := filepath.Join(t.TempDir(), "license-evidence.json")
	var licenseEvidenceSHA256 string
	if reconstructEvidence {
		licenseEvidenceSHA256, err = FreezeLicenseEvidence(ctx, LicenseEvidenceOptions{Commit: commit, Source: source, Out: licenseEvidenceFile})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		licenseEvidenceBody, marshalErr := marshalLicenseEvidence(licenseEvidence)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		writeSigningFixture(t, licenseEvidenceFile, licenseEvidenceBody, 0644)
		licenseEvidenceSHA256 = licenseEvidenceDigest(licenseEvidenceBody)
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "seed")
	writeSigningFixture(t, keyFile, []byte(hex.EncodeToString(private.Seed())+"\n"), 0600)
	keyDigest := sha256.Sum256(public)
	keyFingerprint := "sha256:" + hex.EncodeToString(keyDigest[:])
	record := TrustRecord{
		SchemaVersion: 1, Project: "DarwinRouter", Scope: trustScope, KeyID: "release-test-01",
		Algorithm: "Ed25519", PublicKey: hex.EncodeToString(public), PublicKeySHA256: keyFingerprint,
		Status: "active", PublishedAt: "2026-09-07T00:00:00Z",
		ReleasePolicyURL: "https://example.invalid/release-policy", RotationRevocationURL: "https://example.invalid/key-status",
	}
	trustBody, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	trustBody = append(trustBody, '\n')
	trustFile := filepath.Join(t.TempDir(), "trust.json")
	writeSigningFixture(t, trustFile, trustBody, 0644)
	authorization := SigningAuthorization{
		SchemaVersion: signingAuthorizationSchema, Project: "DarwinRouter", Scope: signingAuthorizationScope,
		CandidateRecordSHA256: prefixedDigest(candidateBody), LicenseEvidenceSHA256: licenseEvidenceSHA256,
		SHA256SUMSSHA256:  prefixedDigest(sumsBody),
		TrustRecordSHA256: prefixedDigest(trustBody), KeyID: record.KeyID, KeyFingerprint: keyFingerprint,
		Targets:    append([]SigningAuthorizationTarget(nil), authorizedTargets...),
		Gates:      append([]SigningAuthorizationGate(nil), signingAuthorizationGates...),
		ApproverID: "test:release-approver", ReleasePolicyURL: record.ReleasePolicyURL,
		ApprovedAt: "2026-09-07T00:01:00Z",
	}
	authorizationBody, err := json.MarshalIndent(authorization, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	authorizationBody = append(authorizationBody, '\n')
	authorizationFile := filepath.Join(t.TempDir(), "authorization.json")
	writeSigningFixture(t, authorizationFile, authorizationBody, 0644)
	return ApprovedSigningOptions{
		Dir: releaseDir, KeyFile: keyFile, CandidateRecordFile: candidateFile,
		ExpectedCandidateSHA256: prefixedDigest(candidateBody), LicenseEvidenceFile: licenseEvidenceFile,
		ExpectedLicenseEvidenceSHA256: licenseEvidenceSHA256, Source: source,
		ExpectedSumsSHA256: prefixedDigest(sumsBody), TrustRecordFile: trustFile,
		ExpectedTrustRecordSHA256: prefixedDigest(trustBody), ExpectedKeyID: record.KeyID,
		ExpectedKeyFingerprint:  keyFingerprint,
		AuthorizationRecordFile: authorizationFile, ExpectedAuthorizationSHA256: prefixedDigest(authorizationBody),
	}, public
}

func driftApprovedSBOMFixture(t *testing.T, body []byte, drift string) []byte {
	t.Helper()
	var document spdxDocument
	if json.Unmarshal(body, &document) != nil {
		t.Fatal("decode approved SBOM fixture")
	}
	replaceRelationID := func(old, replacement string) {
		for i := range document.Relationships {
			if document.Relationships[i].ElementID == old {
				document.Relationships[i].ElementID = replacement
			}
			if document.Relationships[i].RelatedID == old {
				document.Relationships[i].RelatedID = replacement
			}
		}
	}
	switch drift {
	case "created":
		original := document.CreationInfo.Created
		document.CreationInfo.Created = "2026-09-13T18:00:01Z"
		if document.CreationInfo.Created == original {
			document.CreationInfo.Created = "2026-09-13T18:00:02Z"
		}
	case "target":
		document.Name = strings.Replace(document.Name, "darwin-amd64", "darwin-arm64", 1)
		document.DocumentNamespace = strings.Replace(document.DocumentNamespace, "darwin-amd64/sbom", "darwin-arm64/sbom", 1)
	case "binary":
		document.Files[0].Checksums[0].ChecksumValue = strings.Repeat("b", 64)
	case "module":
		for i := 1; i < len(document.Packages); i++ {
			if document.Packages[i].Name != goToolchainModulePath {
				old := document.Packages[i].SPDXID
				document.Packages[i].Name = "example.com/unapproved"
				document.Packages[i].SPDXID = stableSPDXID("Package", document.Packages[i].Name+"@"+document.Packages[i].VersionInfo)
				replaceRelationID(old, document.Packages[i].SPDXID)
				break
			}
		}
	case "toolchain":
		for i := 1; i < len(document.Packages); i++ {
			if document.Packages[i].Name == goToolchainModulePath {
				old := document.Packages[i].SPDXID
				document.Packages[i].VersionInfo = "v1.26.0"
				document.Packages[i].SPDXID = stableSPDXID("Package", goToolchainModulePath+"@"+document.Packages[i].VersionInfo)
				replaceRelationID(old, document.Packages[i].SPDXID)
				break
			}
		}
	case "source_asset":
		document.Files[1].Checksums[0].ChecksumValue = strings.Repeat("b", 64)
	default:
		t.Fatal("unknown SBOM drift fixture", drift)
	}
	sort.Slice(document.Packages[1:], func(i, j int) bool { return document.Packages[i+1].SPDXID < document.Packages[j+1].SPDXID })
	sort.Slice(document.Relationships, func(i, j int) bool {
		a, b := document.Relationships[i], document.Relationships[j]
		return a.ElementID+"\x00"+a.Type+"\x00"+a.RelatedID < b.ElementID+"\x00"+b.Type+"\x00"+b.RelatedID
	})
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if ValidateTargetSBOM(encoded) != nil {
		t.Fatal("drift fixture must remain canonical SPDX", drift)
	}
	return encoded
}

func approvedSourceCommit(t *testing.T, source string) string {
	t.Helper()
	commit, err := command(context.Background(), source, environment(), "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return commit
}
