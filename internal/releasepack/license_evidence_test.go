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
	record, err := deriveLicenseEvidence(t.Context(), root, strings.Repeat("a", 40), environment())
	if err != nil {
		t.Fatal(err)
	}
	body, err := marshalLicenseEvidence(record)
	if err != nil || len(body) == 0 || len(record.Targets) != 4 {
		t.Fatal("repository evidence contract", err)
	}
	for _, target := range record.Targets {
		if len(target.Modules) == 0 || !trustFingerprint(target.NoticeSHA256) {
			t.Fatal("target closure missing", target.OS, target.Arch)
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
		"schema":         func(r *LicenseEvidence) { r.SchemaVersion++ },
		"scope":          func(r *LicenseEvidence) { r.Scope = "other" },
		"commit":         func(r *LicenseEvidence) { r.SourceCommit = "bad" },
		"toolchain":      func(r *LicenseEvidence) { r.Toolchain.GOVERSION = "devel" },
		"go_directive":   func(r *LicenseEvidence) { r.Toolchain.GoDirective = "latest" },
		"license_spdx":   func(r *LicenseEvidence) { r.RootLicense.SPDX = "Apache-2.0" },
		"license_digest": func(r *LicenseEvidence) { r.RootLicense.SHA256 = "bad" },
		"target":         func(r *LicenseEvidence) { r.Targets[0].OS = "windows" },
		"notice":         func(r *LicenseEvidence) { r.Targets[0].NoticeSHA256 = "bad" },
		"module":         func(r *LicenseEvidence) { r.Targets[0].Modules[0].Version = "bad" },
		"file":           func(r *LicenseEvidence) { r.Targets[0].Modules[0].Files[0].Name = "../LICENSE" },
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
		`license-evidence verify --record "$$DARWIN_LICENSE_EVIDENCE_RECORD" --record-sha256 "$$DARWIN_LICENSE_EVIDENCE_SHA256" --source .`,
	} {
		if !strings.Contains(text, required) {
			t.Fatal("make evidence gate weakened", required)
		}
	}
}

func licenseEvidenceRecordFixture() LicenseEvidence {
	file := LicenseEvidenceFile{Name: "LICENSE", Size: 10, SHA256: "sha256:" + strings.Repeat("4", 64)}
	module := LicenseEvidenceModule{Path: "example.com/module", Version: "v1.2.3", Files: []LicenseEvidenceFile{file}}
	targets := make([]LicenseEvidenceTarget, 0, len(licenseEvidenceTargets))
	for _, target := range licenseEvidenceTargets {
		targets = append(targets, LicenseEvidenceTarget{OS: target[0], Arch: target[1], NoticeSHA256: "sha256:" + strings.Repeat("3", 64), Modules: []LicenseEvidenceModule{module}})
	}
	return LicenseEvidence{
		SchemaVersion: 1, Scope: licenseEvidenceScope, SourceCommit: strings.Repeat("1", 40),
		Toolchain:   LicenseEvidenceToolchain{GOVERSION: "go1.27.1", GoDirective: "1.27.1"},
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
