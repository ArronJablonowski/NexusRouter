package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	licenseEvidenceSchema = 3
	maxLicenseEvidence    = 256 << 10
	licenseEvidenceScope  = "darwinrouter-candidate-license-evidence"
)

type LicenseEvidenceOptions struct {
	Commit string
	Source string
	Out    string
}

type LicenseEvidence struct {
	SchemaVersion  int                           `json:"schema_version"`
	Scope          string                        `json:"scope"`
	SourceCommit   string                        `json:"source_commit"`
	SourceInputs   LicenseEvidenceSourceInputs   `json:"source_inputs"`
	Reconstruction LicenseEvidenceReconstruction `json:"reconstruction"`
	Toolchain      LicenseEvidenceToolchain      `json:"toolchain"`
	RootLicense    LicenseEvidenceRoot           `json:"root_license"`
	Targets        []LicenseEvidenceTarget       `json:"targets"`
}

type LicenseEvidenceSourceInputs struct {
	GoModSHA256 string `json:"go_mod_sha256"`
	GoSumSHA256 string `json:"go_sum_sha256"`
}

type LicenseEvidenceReconstruction struct {
	Policy           string `json:"policy"`
	ModuleProxy      string `json:"module_proxy"`
	ChecksumDatabase string `json:"checksum_database"`
	ModuleMode       string `json:"module_mode"`
	ModuleCache      string `json:"module_cache"`
	BuildCache       string `json:"build_cache"`
	NetworkFallback  string `json:"network_fallback"`
	PrivateModules   string `json:"private_modules"`
}

type LicenseEvidenceToolchain struct {
	GOVERSION      string `json:"goversion"`
	GoDirective    string `json:"go_directive"`
	LicenseSHA256  string `json:"license_sha256"`
	PatentsSHA256  string `json:"patents_sha256"`
	IdentitySHA256 string `json:"identity_sha256"`
}

type LicenseEvidenceRoot struct {
	Source string `json:"source"`
	SPDX   string `json:"spdx"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type LicenseEvidenceTarget struct {
	OS                    string                  `json:"os"`
	Arch                  string                  `json:"arch"`
	PackageCount          int                     `json:"package_count"`
	DependencyGraphSHA256 string                  `json:"dependency_graph_sha256"`
	NoticeSHA256          string                  `json:"notice_sha256"`
	Modules               []LicenseEvidenceModule `json:"modules"`
}

type LicenseEvidenceModule struct {
	Path     string                `json:"path"`
	Version  string                `json:"version"`
	Sum      string                `json:"sum"`
	GoModSum string                `json:"go_mod_sum"`
	Files    []LicenseEvidenceFile `json:"files"`
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
	return freezeLicenseEvidenceWithPolicy(ctx, options, productionGoReconstructionPolicy())
}

func freezeLicenseEvidenceWithPolicy(ctx context.Context, options LicenseEvidenceOptions, policy goReconstructionPolicyOptions) (string, error) {
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
	record, err := deriveLicenseEvidenceProtectedWithPolicy(ctx, snapshotDir, options.Commit, env, policy, source, out)
	if err != nil {
		return "", err
	}
	body, err := marshalLicenseEvidenceWithPolicy(record, policy)
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
	_, err := verifyLicenseEvidenceRecordWithPolicy(ctx, recordPath, expectedSHA256, source, productionGoReconstructionPolicy())
	return err
}

// verifyLicenseEvidenceRecord returns the exact canonical record only after it
// has been independently digest-bound and fully re-derived from the clean
// source. Approval-bound release paths use the returned public metadata to
// relate the evidence to candidate and artifact identities.
func verifyLicenseEvidenceRecord(ctx context.Context, recordPath, expectedSHA256, source string, protectedPaths ...string) (LicenseEvidence, error) {
	return verifyLicenseEvidenceRecordWithPolicy(ctx, recordPath, expectedSHA256, source, productionGoReconstructionPolicy(), protectedPaths...)
}

func verifyLicenseEvidenceRecordWithPolicy(ctx context.Context, recordPath, expectedSHA256, source string, policy goReconstructionPolicyOptions, protectedPaths ...string) (LicenseEvidence, error) {
	if ctx == nil || !trustFingerprint(expectedSHA256) || source == "" {
		return LicenseEvidence{}, ErrInvalid
	}
	body, record, err := readLicenseEvidenceWithPolicy(recordPath, policy)
	if err != nil || licenseEvidenceDigest(body) != expectedSHA256 {
		return LicenseEvidence{}, ErrInvalid
	}
	root, err := filepath.Abs(source)
	if err != nil {
		return LicenseEvidence{}, ErrInvalid
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return LicenseEvidence{}, ErrInvalid
	}
	env := environment()
	top, err := command(ctx, root, env, "git", "rev-parse", "--show-toplevel")
	if err != nil || top != root || verifyCandidateCheckout(ctx, root, record.SourceCommit, env) != nil {
		return LicenseEvidence{}, ErrInvalid
	}
	snapshotDir, err := os.MkdirTemp("", ".darwin-license-verify-")
	if err != nil {
		return LicenseEvidence{}, err
	}
	defer os.RemoveAll(snapshotDir)
	if err = snapshot(ctx, root, record.SourceCommit, snapshotDir, env); err != nil {
		return LicenseEvidence{}, err
	}
	protected := append([]string{root, recordPath}, protectedPaths...)
	expected, err := deriveLicenseEvidenceProtectedWithPolicy(ctx, snapshotDir, record.SourceCommit, env, policy, protected...)
	if err != nil {
		return LicenseEvidence{}, err
	}
	expectedBody, err := marshalLicenseEvidenceWithPolicy(expected, policy)
	if err != nil || !bytes.Equal(body, expectedBody) || verifyCandidateCheckout(ctx, root, record.SourceCommit, env) != nil {
		return LicenseEvidence{}, ErrInvalid
	}
	return record, nil
}

func deriveLicenseEvidence(ctx context.Context, source, commit string, env []string) (LicenseEvidence, error) {
	return deriveLicenseEvidenceProtectedWithPolicy(ctx, source, commit, env, productionGoReconstructionPolicy(), source)
}

func deriveLicenseEvidenceProtected(ctx context.Context, source, commit string, env []string, protectedPaths ...string) (record LicenseEvidence, resultErr error) {
	return deriveLicenseEvidenceProtectedWithPolicy(ctx, source, commit, env, productionGoReconstructionPolicy(), protectedPaths...)
}

func deriveLicenseEvidenceProtectedWithPolicy(ctx context.Context, source, commit string, env []string, policy goReconstructionPolicyOptions, protectedPaths ...string) (record LicenseEvidence, resultErr error) {
	if ctx == nil || source == "" || !commitPattern.MatchString(commit) || len(env) == 0 {
		return LicenseEvidence{}, ErrInvalid
	}
	reconstruction, err := newGoReconstructionWithPolicy(env, policy, append([]string{source}, protectedPaths...)...)
	if err != nil {
		return LicenseEvidence{}, ErrInvalid
	}
	defer func() {
		if closeErr := reconstruction.close(); closeErr != nil {
			record = LicenseEvidence{}
			resultErr = ErrInvalid
		}
	}()
	goMod, err := readLicenseEvidenceInput(source, "go.mod", 1<<20)
	if err != nil {
		return LicenseEvidence{}, ErrInvalid
	}
	goSum, err := readLicenseEvidenceInput(source, "go.sum", 8<<20)
	if err != nil {
		return LicenseEvidence{}, ErrInvalid
	}
	goVersion, err := reconstruction.goOutput(ctx, source, reconstruction.env, "env", "GOVERSION")
	if err != nil || !signedToolchain.MatchString(goVersion) || goVersion != runtime.Version() {
		return LicenseEvidence{}, ErrInvalid
	}
	moduleJSON, err := reconstruction.goOutput(ctx, source, reconstruction.env, "mod", "edit", "-json")
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
	toolchain, err := licenseEvidenceToolchain(goVersion, module.Go)
	if err != nil {
		return LicenseEvidence{}, ErrInvalid
	}
	record = LicenseEvidence{
		SchemaVersion: licenseEvidenceSchema, Scope: licenseEvidenceScope, SourceCommit: commit,
		SourceInputs: LicenseEvidenceSourceInputs{GoModSHA256: goMod.digest, GoSumSHA256: goSum.digest},
		Reconstruction: LicenseEvidenceReconstruction{
			Policy: policy.policy, ModuleProxy: policy.moduleProxy,
			ChecksumDatabase: policy.checksumDatabase, ModuleMode: "readonly",
			ModuleCache: "fresh-isolated", BuildCache: "fresh-isolated",
			NetworkFallback: "disabled", PrivateModules: "disabled",
		},
		Toolchain:   toolchain,
		RootLicense: LicenseEvidenceRoot{Source: licenseName, SPDX: "MIT", Size: int64(len(license)), SHA256: licenseEvidenceDigest(license)},
	}
	for _, target := range licenseEvidenceTargets {
		evidence, targetErr := deriveTargetLicenseEvidence(ctx, source, target[0], target[1], reconstruction)
		if targetErr != nil {
			return LicenseEvidence{}, targetErr
		}
		record.Targets = append(record.Targets, evidence)
	}
	if _, err = reconstruction.goOutput(ctx, source, reconstruction.env, "mod", "verify"); err != nil ||
		reconstruction.verify() != nil || goMod.verify() != nil || goSum.verify() != nil {
		return LicenseEvidence{}, ErrInvalid
	}
	return record, nil
}

func deriveTargetLicenseEvidence(ctx context.Context, source, targetOS, targetArch string, reconstruction *goReconstruction) (LicenseEvidenceTarget, error) {
	closure, err := targetNoticeClosure(ctx, source, targetOS, targetArch, reconstruction)
	if err != nil {
		return LicenseEvidenceTarget{}, err
	}
	target := LicenseEvidenceTarget{
		OS: targetOS, Arch: targetArch, PackageCount: closure.PackageCount,
		DependencyGraphSHA256: closure.DependencyGraphSHA256,
	}
	for _, module := range closure.Modules {
		item := LicenseEvidenceModule{Path: module.Path, Version: module.Version, Sum: module.Sum, GoModSum: module.GoModSum}
		for _, file := range module.Files {
			item.Files = append(item.Files, LicenseEvidenceFile{Name: file.Name, Size: int64(len(file.Body)), SHA256: licenseEvidenceDigest(file.Body)})
		}
		target.Modules = append(target.Modules, item)
	}
	notice, err := renderThirdPartyNotices(targetOS, targetArch, closure.Modules)
	if err != nil || validateNotice(notice, targetOS, targetArch) != nil {
		return LicenseEvidenceTarget{}, ErrInvalid
	}
	target.NoticeSHA256 = licenseEvidenceDigest(notice)
	return target, nil
}

func marshalLicenseEvidence(record LicenseEvidence) ([]byte, error) {
	return marshalLicenseEvidenceWithPolicy(record, productionGoReconstructionPolicy())
}

func marshalLicenseEvidenceWithPolicy(record LicenseEvidence, policy goReconstructionPolicyOptions) ([]byte, error) {
	if validateLicenseEvidenceWithPolicy(record, policy) != nil {
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
	return validateLicenseEvidenceWithPolicy(record, productionGoReconstructionPolicy())
}

func validateLicenseEvidenceWithPolicy(record LicenseEvidence, policy goReconstructionPolicyOptions) error {
	if record.SchemaVersion != licenseEvidenceSchema || record.Scope != licenseEvidenceScope || !commitPattern.MatchString(record.SourceCommit) ||
		!trustFingerprint(record.SourceInputs.GoModSHA256) || !trustFingerprint(record.SourceInputs.GoSumSHA256) ||
		!validGoReconstructionPolicy(policy) || record.Reconstruction.Policy != policy.policy || record.Reconstruction.ModuleProxy != policy.moduleProxy ||
		record.Reconstruction.ChecksumDatabase != policy.checksumDatabase || record.Reconstruction.ModuleMode != "readonly" ||
		record.Reconstruction.ModuleCache != "fresh-isolated" || record.Reconstruction.BuildCache != "fresh-isolated" ||
		record.Reconstruction.NetworkFallback != "disabled" || record.Reconstruction.PrivateModules != "disabled" ||
		!signedToolchain.MatchString(record.Toolchain.GOVERSION) || !goDirectivePattern.MatchString(record.Toolchain.GoDirective) ||
		!trustFingerprint(record.Toolchain.LicenseSHA256) || !trustFingerprint(record.Toolchain.PatentsSHA256) ||
		!trustFingerprint(record.Toolchain.IdentitySHA256) || validLicenseEvidenceToolchainIdentity(record.Toolchain) != nil ||
		record.RootLicense.Source != licenseName || record.RootLicense.SPDX != "MIT" || record.RootLicense.Size < 1 || record.RootLicense.Size > maxLicense ||
		!trustFingerprint(record.RootLicense.SHA256) || len(record.Targets) != len(licenseEvidenceTargets) {
		return ErrInvalid
	}
	for i, expected := range licenseEvidenceTargets {
		target := record.Targets[i]
		if target.OS != expected[0] || target.Arch != expected[1] || target.PackageCount < 1 || target.PackageCount > 100_000 ||
			!trustFingerprint(target.DependencyGraphSHA256) || !trustFingerprint(target.NoticeSHA256) || len(target.Modules) == 0 || len(target.Modules) > 10_000 {
			return ErrInvalid
		}
		previous := ""
		toolchainCount := 0
		for _, module := range target.Modules {
			key := module.Path + "@" + module.Version
			toolchain := module.Path == goToolchainModulePath
			if !safeNoticeModule(module.Path, module.Version) || key <= previous || len(module.Files) == 0 || len(module.Files) > 1_000 ||
				(toolchain && (module.Sum != "" || module.GoModSum != "")) || (!toolchain && (!validModuleSum(module.Sum) || !validModuleSum(module.GoModSum))) {
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
			if toolchain {
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
		module.Version != "v"+strings.TrimPrefix(goVersion, "go") || module.Sum != "" || module.GoModSum != "" || len(module.Files) != 2 {
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

func validModuleSum(value string) bool {
	if !strings.HasPrefix(value, "h1:") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "h1:"))
	return err == nil && len(decoded) == sha256.Size && value == "h1:"+base64.StdEncoding.EncodeToString(decoded)
}

func licenseEvidenceToolchain(goVersion, goDirective string) (LicenseEvidenceToolchain, error) {
	toolchain := LicenseEvidenceToolchain{
		GOVERSION: goVersion, GoDirective: goDirective,
		LicenseSHA256: licenseEvidenceDigest([]byte(goLicenseText)),
		PatentsSHA256: licenseEvidenceDigest([]byte(goPatentsText)),
	}
	body, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		GOVERSION     string `json:"goversion"`
		GoDirective   string `json:"go_directive"`
		LicenseSHA256 string `json:"license_sha256"`
		PatentsSHA256 string `json:"patents_sha256"`
	}{1, toolchain.GOVERSION, toolchain.GoDirective, toolchain.LicenseSHA256, toolchain.PatentsSHA256})
	if err != nil {
		return LicenseEvidenceToolchain{}, ErrInvalid
	}
	toolchain.IdentitySHA256 = licenseEvidenceDigest(append(body, '\n'))
	if validLicenseEvidenceToolchainIdentity(toolchain) != nil {
		return LicenseEvidenceToolchain{}, ErrInvalid
	}
	return toolchain, nil
}

func validLicenseEvidenceToolchainIdentity(toolchain LicenseEvidenceToolchain) error {
	if !signedToolchain.MatchString(toolchain.GOVERSION) || !goDirectivePattern.MatchString(toolchain.GoDirective) ||
		toolchain.LicenseSHA256 != licenseEvidenceDigest([]byte(goLicenseText)) ||
		toolchain.PatentsSHA256 != licenseEvidenceDigest([]byte(goPatentsText)) {
		return ErrInvalid
	}
	body, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		GOVERSION     string `json:"goversion"`
		GoDirective   string `json:"go_directive"`
		LicenseSHA256 string `json:"license_sha256"`
		PatentsSHA256 string `json:"patents_sha256"`
	}{1, toolchain.GOVERSION, toolchain.GoDirective, toolchain.LicenseSHA256, toolchain.PatentsSHA256})
	if err != nil || toolchain.IdentitySHA256 != licenseEvidenceDigest(append(body, '\n')) {
		return ErrInvalid
	}
	return nil
}

func readLicenseEvidence(path string) ([]byte, LicenseEvidence, error) {
	return readLicenseEvidenceWithPolicy(path, productionGoReconstructionPolicy())
}

func readLicenseEvidenceWithPolicy(path string, policy goReconstructionPolicyOptions) ([]byte, LicenseEvidence, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxLicenseEvidence {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		file.Close()
		return nil, LicenseEvidence{}, ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(file, maxLicenseEvidence+1))
	final, finalErr := file.Stat()
	closeErr := file.Close()
	if err != nil || finalErr != nil || closeErr != nil || !os.SameFile(actual, final) ||
		final.Size() != actual.Size() || int64(len(body)) != actual.Size() {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	var record LicenseEvidence
	if json.Unmarshal(body, &record) != nil || validateLicenseEvidenceWithPolicy(record, policy) != nil {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	canonical, err := marshalLicenseEvidenceWithPolicy(record, policy)
	if err != nil || !bytes.Equal(body, canonical) {
		return nil, LicenseEvidence{}, ErrInvalid
	}
	return body, record, nil
}

func readRootEvidenceFile(root *os.Root, name string, max int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > max {
		return nil, ErrInvalid
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, ErrInvalid
	}
	actual, statErr := file.Stat()
	body, readErr := io.ReadAll(io.LimitReader(file, max+1))
	final, finalErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || readErr != nil || finalErr != nil || closeErr != nil ||
		!actual.Mode().IsRegular() || !os.SameFile(info, actual) || !os.SameFile(actual, final) ||
		actual.Size() != info.Size() || final.Size() != actual.Size() || int64(len(body)) != actual.Size() {
		return nil, ErrInvalid
	}
	return body, nil
}

type licenseEvidenceInput struct {
	path   string
	info   os.FileInfo
	size   int64
	digest string
}

func readLicenseEvidenceInput(source, name string, max int64) (licenseEvidenceInput, error) {
	if filepath.Base(name) != name || max < 1 {
		return licenseEvidenceInput{}, ErrInvalid
	}
	path := filepath.Join(source, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > max {
		return licenseEvidenceInput{}, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return licenseEvidenceInput{}, ErrInvalid
	}
	actual, statErr := file.Stat()
	body, readErr := io.ReadAll(io.LimitReader(file, max+1))
	final, finalErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || readErr != nil || finalErr != nil || closeErr != nil ||
		!actual.Mode().IsRegular() || !os.SameFile(info, actual) || !os.SameFile(actual, final) ||
		actual.Size() != info.Size() || final.Size() != actual.Size() || int64(len(body)) != actual.Size() {
		return licenseEvidenceInput{}, ErrInvalid
	}
	return licenseEvidenceInput{path: path, info: info, size: info.Size(), digest: licenseEvidenceDigest(body)}, nil
}

func (input licenseEvidenceInput) verify() error {
	if input.path == "" || input.info == nil || input.size < 1 || !trustFingerprint(input.digest) {
		return ErrInvalid
	}
	info, err := os.Lstat(input.path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != input.size || !os.SameFile(input.info, info) {
		return ErrInvalid
	}
	file, err := os.Open(input.path)
	if err != nil {
		return ErrInvalid
	}
	body, readErr := io.ReadAll(io.LimitReader(file, input.size+1))
	final, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || !os.SameFile(info, final) ||
		int64(len(body)) != input.size || licenseEvidenceDigest(body) != input.digest {
		return ErrInvalid
	}
	return nil
}

var goDirectivePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))?$`)

func licenseEvidenceDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}
