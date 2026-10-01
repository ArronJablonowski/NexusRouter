package releasepack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryLicenseEvidenceDerivation(t *testing.T) {
	root, err := command(t.Context(), ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	ambientCache := t.TempDir()
	marker := filepath.Join(ambientCache, "must-not-be-consulted")
	if err = os.WriteFile(marker, []byte("ambient\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deriveEnv := append(environment(), "GOMODCACHE="+ambientCache, "GOCACHE="+ambientCache, "GOPROXY=off", "GOSUMDB=off")
	record, err := deriveLicenseEvidence(t.Context(), root, strings.Repeat("a", 40), deriveEnv)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(ambientCache)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(marker) {
		t.Fatal("ambient user cache was consulted", err)
	}
	raw, _ := json.MarshalIndent(record, "", "  ")
	t.Logf("record bytes=%d limit=%d", len(raw)+1, maxLicenseEvidence)
	body, err := marshalLicenseEvidence(record)
	if err != nil || len(body) == 0 || len(record.Targets) != 4 {
		t.Fatal("repository evidence contract", err)
	}
	for _, target := range record.Targets {
		if len(target.Modules) == 0 || !trustFingerprint(target.NoticeSHA256) {
			t.Fatal("target closure missing", target.OS, target.Arch)
		}
		toolchains := 0
		for _, module := range target.Modules {
			if module.Path == goToolchainModulePath {
				toolchains++
				if !validLicenseEvidenceToolchain(module, record.Toolchain.GOVERSION) {
					t.Fatal("exact Go toolchain legal evidence missing", target.OS, target.Arch)
				}
			}
		}
		if toolchains != 1 {
			t.Fatal("expected exactly one Go toolchain evidence module", target.OS, target.Arch, toolchains)
		}
	}
}

func TestLicenseEvidenceCanonicalContractRejectsDrift(t *testing.T) {
	record := licenseEvidenceRecordFixture()
	body, err := marshalLicenseEvidence(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err = os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	readBody, parsed, err := readLicenseEvidence(path)
	if err != nil || string(readBody) != string(body) || parsed.SourceCommit != record.SourceCommit {
		t.Fatal("canonical evidence rejected", err)
	}
	for name, mutate := range map[string]func(*LicenseEvidence){
		"schema":       func(r *LicenseEvidence) { r.SchemaVersion++ },
		"schema_2":     func(r *LicenseEvidence) { r.SchemaVersion = 2 },
		"scope":        func(r *LicenseEvidence) { r.Scope = "other" },
		"commit":       func(r *LicenseEvidence) { r.SourceCommit = "bad" },
		"go_mod":       func(r *LicenseEvidence) { r.SourceInputs.GoModSHA256 = "bad" },
		"go_sum":       func(r *LicenseEvidence) { r.SourceInputs.GoSumSHA256 = "bad" },
		"policy":       func(r *LicenseEvidence) { r.Reconstruction.Policy = "other" },
		"proxy":        func(r *LicenseEvidence) { r.Reconstruction.ModuleProxy = "direct" },
		"sumdb":        func(r *LicenseEvidence) { r.Reconstruction.ChecksumDatabase = "off" },
		"module_mode":  func(r *LicenseEvidence) { r.Reconstruction.ModuleMode = "mod" },
		"module_cache": func(r *LicenseEvidence) { r.Reconstruction.ModuleCache = "ambient" },
		"build_cache":  func(r *LicenseEvidence) { r.Reconstruction.BuildCache = "ambient" },
		"fallback":     func(r *LicenseEvidence) { r.Reconstruction.NetworkFallback = "direct" },
		"private":      func(r *LicenseEvidence) { r.Reconstruction.PrivateModules = "enabled" },
		"toolchain":    func(r *LicenseEvidence) { r.Toolchain.GOVERSION = "devel" },
		"go_directive": func(r *LicenseEvidence) { r.Toolchain.GoDirective = "latest" },
		"toolchain_license": func(r *LicenseEvidence) {
			r.Toolchain.LicenseSHA256 = "bad"
		},
		"toolchain_patents": func(r *LicenseEvidence) {
			r.Toolchain.PatentsSHA256 = "bad"
		},
		"toolchain_id":   func(r *LicenseEvidence) { r.Toolchain.IdentitySHA256 = "bad" },
		"license_spdx":   func(r *LicenseEvidence) { r.RootLicense.SPDX = "Apache-2.0" },
		"license_digest": func(r *LicenseEvidence) { r.RootLicense.SHA256 = "bad" },
		"target":         func(r *LicenseEvidence) { r.Targets[0].OS = "windows" },
		"package_count":  func(r *LicenseEvidence) { r.Targets[0].PackageCount = 0 },
		"graph":          func(r *LicenseEvidence) { r.Targets[0].DependencyGraphSHA256 = "bad" },
		"notice":         func(r *LicenseEvidence) { r.Targets[0].NoticeSHA256 = "bad" },
		"module":         func(r *LicenseEvidence) { r.Targets[0].Modules[0].Version = "bad" },
		"module_sum":     func(r *LicenseEvidence) { r.Targets[0].Modules[0].Sum = "bad" },
		"go_mod_sum":     func(r *LicenseEvidence) { r.Targets[0].Modules[0].GoModSum = "bad" },
		"file":           func(r *LicenseEvidence) { r.Targets[0].Modules[0].Files[0].Name = "../LICENSE" },
		"toolchain_missing": func(r *LicenseEvidence) {
			r.Targets[0].Modules = r.Targets[0].Modules[:len(r.Targets[0].Modules)-1]
		},
		"toolchain_license_digest": func(r *LicenseEvidence) {
			r.Targets[0].Modules[len(r.Targets[0].Modules)-1].Files[0].SHA256 = "sha256:" + strings.Repeat("0", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneLicenseEvidence(record)
			mutate(&changed)
			if _, err := marshalLicenseEvidence(changed); err == nil {
				t.Fatal("contract drift accepted")
			}
		})
	}
	for _, invalid := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), '\n'), []byte("{}\n")} {
		invalidPath := filepath.Join(t.TempDir(), "invalid.json")
		if err = os.WriteFile(invalidPath, invalid, 0644); err != nil {
			t.Fatal(err)
		}
		if _, _, err = readLicenseEvidence(invalidPath); err == nil {
			t.Fatal("noncanonical evidence accepted")
		}
	}
}

func TestFreezeAndVerifyLicenseEvidenceFromCleanCommit(t *testing.T) {
	root, err := command(t.Context(), ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	sourceParent := t.TempDir()
	source := filepath.Join(sourceParent, "source")
	if _, err = command(t.Context(), sourceParent, environment(), "git", "clone", "--quiet", "--no-local", root, source); err != nil {
		t.Fatal(err)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := command(t.Context(), source, environment(), "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "license-evidence.json")
	digest, err := FreezeLicenseEvidence(t.Context(), LicenseEvidenceOptions{Commit: commit, Source: source, Out: out})
	if err != nil || !trustFingerprint(digest) {
		t.Fatal("freeze", err)
	}
	if err = VerifyLicenseEvidence(t.Context(), out, digest, source); err != nil {
		t.Fatal("verify", err)
	}
	licensePath := filepath.Join(source, licenseName)
	originalLicense, err := os.ReadFile(licensePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(licensePath, append(append([]byte(nil), originalLicense...), '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	if err = VerifyLicenseEvidence(t.Context(), out, digest, source); err == nil {
		t.Fatal("dirty license drift accepted")
	}
	if err = os.WriteFile(licensePath, originalLicense, 0644); err != nil {
		t.Fatal(err)
	}
	if err = VerifyLicenseEvidence(t.Context(), out, "sha256:"+strings.Repeat("0", 64), source); err == nil {
		t.Fatal("wrong independent digest accepted")
	}
	body, record, err := readLicenseEvidence(out)
	if err != nil {
		t.Fatal(err)
	}
	record.RootLicense.SHA256 = "sha256:" + strings.Repeat("0", 64)
	tampered, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	tampered = append(tampered, '\n')
	tamperedPath := filepath.Join(t.TempDir(), "tampered.json")
	if err = os.WriteFile(tamperedPath, tampered, 0644); err != nil {
		t.Fatal(err)
	}
	if err = VerifyLicenseEvidence(t.Context(), tamperedPath, licenseEvidenceDigest(tampered), source); err == nil {
		t.Fatal("candidate license drift accepted")
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err = os.Symlink(out, link); err != nil {
		t.Fatal(err)
	}
	if err = VerifyLicenseEvidence(t.Context(), link, licenseEvidenceDigest(body), source); err == nil {
		t.Fatal("symlink record accepted")
	}
}

func TestLicenseEvidenceMakeGateIsFailClosed(t *testing.T) {
	body, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"qualify-license-evidence:",
		`test -n "$$DARWIN_LICENSE_EVIDENCE_RECORD"`,
		`test -n "$$DARWIN_LICENSE_EVIDENCE_SHA256"`,
		`scripts/license-evidence-bootstrap.sh verify --record "$$DARWIN_LICENSE_EVIDENCE_RECORD" --record-sha256 "$$DARWIN_LICENSE_EVIDENCE_SHA256" --source .`,
	} {
		if !strings.Contains(text, required) {
			t.Fatal("make evidence gate weakened", required)
		}
	}
	if strings.Contains(text, `go run ./cmd/license-evidence verify`) {
		t.Fatal("make evidence gate consults the ambient Go bootstrap cache")
	}
}

func TestLicenseEvidenceSourceInputPinsExactRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go.mod")
	if err := os.WriteFile(path, []byte("module example.com/test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	input, err := readLicenseEvidenceInput(root, "go.mod", 1<<20)
	if err != nil || input.verify() != nil || !trustFingerprint(input.digest) {
		t.Fatal("regular source input rejected", err)
	}
	if err = os.WriteFile(path, []byte("module example.com/drift\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if input.verify() == nil {
		t.Fatal("source input content drift accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "target"), path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "target"), []byte("module example.com/test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = readLicenseEvidenceInput(root, "go.mod", 1<<20); err == nil {
		t.Fatal("symlink source input accepted")
	}
}

func TestLicenseEvidenceToolchainIdentityIsHostNeutralAndExact(t *testing.T) {
	toolchain, err := licenseEvidenceToolchain("go1.27.1", "1.27.1")
	if err != nil || validLicenseEvidenceToolchainIdentity(toolchain) != nil {
		t.Fatal("canonical toolchain identity rejected", err)
	}
	for _, mutate := range []func(*LicenseEvidenceToolchain){
		func(value *LicenseEvidenceToolchain) { value.GOVERSION = "go1.27.2" },
		func(value *LicenseEvidenceToolchain) { value.GoDirective = "1.27.0" },
		func(value *LicenseEvidenceToolchain) { value.LicenseSHA256 = invalidPublicDigest() },
		func(value *LicenseEvidenceToolchain) { value.PatentsSHA256 = invalidPublicDigest() },
		func(value *LicenseEvidenceToolchain) { value.IdentitySHA256 = invalidPublicDigest() },
	} {
		changed := toolchain
		mutate(&changed)
		if validLicenseEvidenceToolchainIdentity(changed) == nil {
			t.Fatal("toolchain identity drift accepted")
		}
	}
}

func licenseEvidenceRecordFixture() LicenseEvidence {
	file := LicenseEvidenceFile{Name: "LICENSE", Size: 10, SHA256: "sha256:" + strings.Repeat("4", 64)}
	moduleSum := "h1:" + strings.Repeat("A", 43) + "="
	module := LicenseEvidenceModule{Path: "example.com/module", Version: "v1.2.3", Sum: moduleSum, GoModSum: moduleSum, Files: []LicenseEvidenceFile{file}}
	toolchain := LicenseEvidenceModule{Path: goToolchainModulePath, Version: "v1.27.1", Files: []LicenseEvidenceFile{
		{Name: "LICENSE", Size: int64(len(goLicenseText)), SHA256: licenseEvidenceDigest([]byte(goLicenseText))},
		{Name: "PATENTS", Size: int64(len(goPatentsText)), SHA256: licenseEvidenceDigest([]byte(goPatentsText))},
	}}
	toolchainEvidence, _ := licenseEvidenceToolchain("go1.27.1", "1.27.1")
	targets := make([]LicenseEvidenceTarget, 0, len(licenseEvidenceTargets))
	for _, target := range licenseEvidenceTargets {
		targets = append(targets, LicenseEvidenceTarget{
			OS: target[0], Arch: target[1], PackageCount: 10,
			DependencyGraphSHA256: "sha256:" + strings.Repeat("5", 64),
			NoticeSHA256:          "sha256:" + strings.Repeat("3", 64), Modules: []LicenseEvidenceModule{module, toolchain},
		})
	}
	return LicenseEvidence{
		SchemaVersion: licenseEvidenceSchema, Scope: licenseEvidenceScope, SourceCommit: strings.Repeat("1", 40),
		SourceInputs: LicenseEvidenceSourceInputs{GoModSHA256: "sha256:" + strings.Repeat("6", 64), GoSumSHA256: "sha256:" + strings.Repeat("7", 64)},
		Reconstruction: LicenseEvidenceReconstruction{
			Policy: goReconstructionPolicy, ModuleProxy: goReconstructionProxy, ChecksumDatabase: goReconstructionSumDatabase,
			ModuleMode: "readonly", ModuleCache: "fresh-isolated", BuildCache: "fresh-isolated", NetworkFallback: "disabled", PrivateModules: "disabled",
		},
		Toolchain:   toolchainEvidence,
		RootLicense: LicenseEvidenceRoot{Source: "LICENSE", SPDX: "MIT", Size: 21, SHA256: "sha256:" + strings.Repeat("2", 64)},
		Targets:     targets,
	}
}

func cloneLicenseEvidence(record LicenseEvidence) LicenseEvidence {
	body, _ := json.Marshal(record)
	var clone LicenseEvidence
	_ = json.Unmarshal(body, &clone)
	return clone
}
