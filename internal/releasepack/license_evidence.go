package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	licenseEvidenceSchema = 2
	maxLicenseEvidence    = 256 << 10
	licenseEvidenceScope  = "darwinrouter-candidate-license-evidence"
)

type LicenseEvidenceOptions struct {
	Commit string
	Source string
	Out    string
}

type LicenseEvidence struct {
	SchemaVersion int                      `json:"schema_version"`
	Scope         string                   `json:"scope"`
	SourceCommit  string                   `json:"source_commit"`
	Toolchain     LicenseEvidenceToolchain `json:"toolchain"`
	RootLicense   LicenseEvidenceRoot      `json:"root_license"`
	Targets       []LicenseEvidenceTarget  `json:"targets"`
}

type LicenseEvidenceToolchain struct {
	GOVERSION   string `json:"goversion"`
	GoDirective string `json:"go_directive"`
}

type LicenseEvidenceRoot struct {
	Source string `json:"source"`
	SPDX   string `json:"spdx"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type LicenseEvidenceTarget struct {
	OS           string                  `json:"os"`
	Arch         string                  `json:"arch"`
	NoticeSHA256 string                  `json:"notice_sha256"`
	Modules      []LicenseEvidenceModule `json:"modules"`
}

type LicenseEvidenceModule struct {
	Path    string                `json:"path"`
	Version string                `json:"version"`
	Files   []LicenseEvidenceFile `json:"files"`
}

type LicenseEvidenceFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var licenseEvidenceTargets = [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}

// FreezeLicenseEvidence derives one canonical record from an immutable commit
// snapshot and exclusively writes it outside the source checkout.
func FreezeLicenseEvidence(ctx context.Context, options LicenseEvidenceOptions) (string, error) {
	if ctx == nil || !commitPattern.MatchString(options.Commit) || options.Source == "" || options.Out == "" {
		return "", ErrInvalid
	}
	source, err := filepath.Abs(options.Source)
	if err != nil {
		return "", ErrInvalid
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", ErrInvalid
	}
	out, err := filepath.Abs(options.Out)
	if err != nil {
		return "", ErrInvalid
	}
	outputRoot, outputName, err := candidateOutputRoot(out, source)
	if err != nil {
		return "", err
	}
	defer outputRoot.Close()
	env := environment()
	top, err := command(ctx, source, env, "git", "rev-parse", "--show-toplevel")
	if err != nil || top != source || verifyCandidateCheckout(ctx, source, options.Commit, env) != nil {
		return "", ErrInvalid
	}
	snapshotDir, err := os.MkdirTemp(filepath.Dir(out), ".darwin-license-source-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(snapshotDir)
	if err = snapshot(ctx, source, options.Commit, snapshotDir, env); err != nil {
		return "", err
	}
	record, err := deriveLicenseEvidence(ctx, snapshotDir, options.Commit, env)
	if err != nil {
		return "", err
	}
	body, err := marshalLicenseEvidence(record)
	if err != nil || verifyCandidateCheckout(ctx, source, options.Commit, env) != nil {
		return "", ErrInvalid
	}
	file, err := outputRoot.OpenFile(outputName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}
	_, writeErr := file.Write(body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return licenseEvidenceDigest(body), nil
}

// VerifyLicenseEvidence binds the exact external record digest and re-derives
// every field from its immutable commit in the supplied clean checkout.
func VerifyLicenseEvidence(ctx context.Context, recordPath, expectedSHA256, source string) error {
	if ctx == nil || !trustFingerprint(expectedSHA256) || source == "" {
		return ErrInvalid
	}
	body, record, err := readLicenseEvidence(recordPath)
	if err != nil || licenseEvidenceDigest(body) != expectedSHA256 {
		return ErrInvalid
	}
	root, err := filepath.Abs(source)
	if err != nil {
		return ErrInvalid
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return ErrInvalid
	}
	env := environment()
	top, err := command(ctx, root, env, "git", "rev-parse", "--show-toplevel")
	if err != nil || top != root || verifyCandidateCheckout(ctx, root, record.SourceCommit, env) != nil {
		return ErrInvalid
	}
	snapshotDir, err := os.MkdirTemp("", ".darwin-license-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(snapshotDir)
	if err = snapshot(ctx, root, record.SourceCommit, snapshotDir, env); err != nil {
		return err
	}
	expected, err := deriveLicenseEvidence(ctx, snapshotDir, record.SourceCommit, env)
	if err != nil {
		return err
	}
	expectedBody, err := marshalLicenseEvidence(expected)
	if err != nil || !bytes.Equal(body, expectedBody) || verifyCandidateCheckout(ctx, root, record.SourceCommit, env) != nil {
		return ErrInvalid
	}
	return nil
}

func deriveLicenseEvidence(ctx context.Context, source, commit string, env []string) (LicenseEvidence, error) {
	if _, err := command(ctx, source, env, "go", "mod", "verify"); err != nil {
		return LicenseEvidence{}, err
	}
	goVersion, err := command(ctx, source, env, "go", "env", "GOVERSION")
	if err != nil || !signedToolchain.MatchString(goVersion) || goVersion != runtime.Version() {
		return LicenseEvidence{}, ErrInvalid
	}
	moduleJSON, err := command(ctx, source, env, "go", "mod", "edit", "-json")
	if err != nil {
		return LicenseEvidence{}, err
	}
	var module struct{ Go string }
	if json.Unmarshal([]byte(moduleJSON), &module) != nil || !goDirectivePattern.MatchString(module.Go) {
		return LicenseEvidence{}, ErrInvalid
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return LicenseEvidence{}, err
	}
	license, err := readRootEvidenceFile(root, licenseName, maxLicense)
	root.Close()
	if err != nil || !bytes.HasPrefix(license, []byte("MIT License\n")) {
		return LicenseEvidence{}, ErrInvalid
	}
	record := LicenseEvidence{
		SchemaVersion: licenseEvidenceSchema, Scope: licenseEvidenceScope, SourceCommit: commit,
		Toolchain:   LicenseEvidenceToolchain{GOVERSION: goVersion, GoDirective: module.Go},
		RootLicense: LicenseEvidenceRoot{Source: licenseName, SPDX: "MIT", Size: int64(len(license)), SHA256: licenseEvidenceDigest(license)},
	}
	for _, target := range licenseEvidenceTargets {
		evidence, targetErr := deriveTargetLicenseEvidence(ctx, source, target[0], target[1], env)
		if targetErr != nil {
			return LicenseEvidence{}, targetErr
		}
		record.Targets = append(record.Targets, evidence)
	}
	if _, err = command(ctx, source, env, "go", "mod", "verify"); err != nil {
		return LicenseEvidence{}, err
	}
	return record, nil
}

func deriveTargetLicenseEvidence(ctx context.Context, source, targetOS, targetArch string, env []string) (LicenseEvidenceTarget, error) {
	modules, err := targetNoticeModules(ctx, source, targetOS, targetArch, env)
	if err != nil {
		return LicenseEvidenceTarget{}, err
	}
	target := LicenseEvidenceTarget{OS: targetOS, Arch: targetArch}
	for _, module := range modules {
		item := LicenseEvidenceModule{Path: module.Path, Version: module.Version}
		for _, file := range module.Files {
			item.Files = append(item.Files, LicenseEvidenceFile{Name: file.Name, Size: int64(len(file.Body)), SHA256: licenseEvidenceDigest(file.Body)})
		}
		target.Modules = append(target.Modules, item)
	}
	notice, err := renderThirdPartyNotices(targetOS, targetArch, modules)
	if err != nil || validateNotice(notice, targetOS, targetArch) != nil {
		return LicenseEvidenceTarget{}, ErrInvalid
	}
	target.NoticeSHA256 = licenseEvidenceDigest(notice)
	return target, nil
}

func marshalLicenseEvidence(record LicenseEvidence) ([]byte, error) {
	if validateLicenseEvidence(record) != nil {
		return nil, ErrInvalid
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	if len(body) > maxLicenseEvidence {
		return nil, ErrInvalid
	}
	return body, nil
}

func validateLicenseEvidence(record LicenseEvidence) error {
	if record.SchemaVersion != licenseEvidenceSchema || record.Scope != licenseEvidenceScope || !commitPattern.MatchString(record.SourceCommit) ||
		!signedToolchain.MatchString(record.Toolchain.GOVERSION) || !goDirectivePattern.MatchString(record.Toolchain.GoDirective) ||
		record.RootLicense.Source != licenseName || record.RootLicense.SPDX != "MIT" || record.RootLicense.Size < 1 || record.RootLicense.Size > maxLicense ||
		!trustFingerprint(record.RootLicense.SHA256) || len(record.Targets) != len(licenseEvidenceTargets) {
		return ErrInvalid
	}
	for i, expected := range licenseEvidenceTargets {
		target := record.Targets[i]
		if target.OS != expected[0] || target.Arch != expected[1] || !trustFingerprint(target.NoticeSHA256) || len(target.Modules) == 0 || len(target.Modules) > 10_000 {
			return ErrInvalid
		}
		previous := ""
		toolchainCount := 0
		for _, module := range target.Modules {
			key := module.Path + "@" + module.Version
			if !safeNoticeModule(module.Path, module.Version) || key <= previous || len(module.Files) == 0 || len(module.Files) > 1_000 {
				return ErrInvalid
			}
			previous = key
			previousFile, hasLicense := "", false
			for _, file := range module.Files {
				upper := strings.ToUpper(file.Name)
				if !safeNoticeFilename(file.Name) || file.Name <= previousFile || file.Size < 1 || file.Size > maxNotice || !trustFingerprint(file.SHA256) {
					return ErrInvalid
				}
				previousFile = file.Name
				hasLicense = hasLicense || strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING")
			}
			if !hasLicense {
				return ErrInvalid
			}
			if module.Path == goToolchainModulePath {
				if !validLicenseEvidenceToolchain(module, record.Toolchain.GOVERSION) {
					return ErrInvalid
				}
				toolchainCount++
			}
		}
		if toolchainCount != 1 {
			return ErrInvalid
		}
	}
	return nil
}

func validLicenseEvidenceToolchain(module LicenseEvidenceModule, goVersion string) bool {
	if !goReleaseVersion.MatchString(goVersion) || module.Path != goToolchainModulePath ||
		module.Version != "v"+strings.TrimPrefix(goVersion, "go") || len(module.Files) != 2 {
		return false
	}
	expected := []noticeFile{{Name: "LICENSE", Body: []byte(goLicenseText)}, {Name: "PATENTS", Body: []byte(goPatentsText)}}
	for i, file := range module.Files {
		if file.Name != expected[i].Name || file.Size != int64(len(expected[i].Body)) ||
			file.SHA256 != licenseEvidenceDigest(expected[i].Body) {
			return false
		}
	}
	return true
}

func readLicenseEvidence(path string) ([]byte, LicenseEvidence, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxLicenseEvidence {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(file, maxLicenseEvidence+1))
	if err != nil || int64(len(body)) != actual.Size() {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	var record LicenseEvidence
	if json.Unmarshal(body, &record) != nil || validateLicenseEvidence(record) != nil {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	canonical, err := marshalLicenseEvidence(record)
	if err != nil || !bytes.Equal(body, canonical) {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	return body, record, nil
}

func readRootEvidenceFile(root *os.Root, name string, max int64) ([]byte, error) {
	info, err := root.Stat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > max {
		return nil, ErrInvalid
	}
	body, err := root.ReadFile(name)
	if err != nil || int64(len(body)) != info.Size() {
		return nil, ErrInvalid
	}
	return body, nil
}

var goDirectivePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))?$`)

func licenseEvidenceDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}
